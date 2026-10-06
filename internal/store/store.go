package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/metrics"
)

var Module = fx.Module("store", fx.Provide(New), fx.Invoke(registerBacklogCollector))

func registerBacklogCollector(reg prometheus.Registerer, st *Store, log *slog.Logger) {
	reg.MustRegister(metrics.NewBacklogCollector(st.Backlog, log))
}

const maxTxAttempts = 3

var (
	ErrNotFound         = errors.New("not found")
	ErrConcurrentUpdate = errors.New("concurrent update detected")
	ErrUnavailable      = errors.New("database temporarily unavailable")
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Repos struct {
	Wallets      WalletRepo
	Transactions TransactionRepo
	Ledger       LedgerRepo
	Outbox       OutboxRepo
	Inbox        InboxRepo
}

func newRepos(q querier) *Repos {
	return &Repos{
		Wallets:      WalletRepo{q: q},
		Transactions: TransactionRepo{q: q},
		Ledger:       LedgerRepo{q: q},
		Outbox:       OutboxRepo{q: q},
		Inbox:        InboxRepo{q: q},
	}
}

type Store struct {
	pool    *pgxpool.Pool
	log     *slog.Logger
	metrics *metrics.Metrics
}

func New(pool *pgxpool.Pool, m *metrics.Metrics, log *slog.Logger) *Store {
	return &Store{pool: pool, log: log, metrics: m}
}

func (s *Store) Read() *Repos {
	return newRepos(s.pool)
}

// InTx runs fn inside a single READ COMMITTED transaction: everything fn does
// through r is committed atomically or rolled back. The whole fn is retried
// on serialization failures, deadlocks and ErrConcurrentUpdate, so fn must not
// have side effects outside the transaction.
func (s *Store) InTx(ctx context.Context, fn func(r *Repos) error) error {
	var err error
	for attempt := 1; attempt <= maxTxAttempts; attempt++ {
		err = s.runTx(ctx, fn)
		reason, retryable := conflictReason(err)
		if !retryable {
			return Classify(ctx, err)
		}
		s.metrics.ConcurrencyConflicts.WithLabelValues(reason).Inc()
		s.log.WarnContext(ctx, "transaction conflict, retrying", "reason", reason, "attempt", attempt)
		if attempt < maxTxAttempts {
			if sleepErr := backoff(ctx, attempt); sleepErr != nil {
				return sleepErr
			}
		}
	}
	return fmt.Errorf("%w: %w", ErrConcurrentUpdate, err)
}

func (s *Store) Backlog(ctx context.Context) (metrics.Backlog, error) {
	var b metrics.Backlog
	var oldestAgeSeconds float64
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM outbox_events WHERE published_at IS NULL),
		(SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(occurred_at)), 0)::float8 FROM outbox_events WHERE published_at IS NULL),
		(SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE')`).
		Scan(&b.PendingEvents, &oldestAgeSeconds, &b.WaitingForReferences)
	b.OldestPendingAge = time.Duration(max(oldestAgeSeconds, 0) * float64(time.Second))
	return b, err
}

func (s *Store) ReadSnapshot(ctx context.Context, fn func(r *Repos) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Classify(ctx, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(newRepos(tx)); err != nil {
		return Classify(ctx, err)
	}
	return Classify(ctx, tx.Commit(ctx))
}

func (s *Store) runTx(ctx context.Context, fn func(r *Repos) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(newRepos(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func conflictReason(err error) (string, bool) {
	if errors.Is(err, ErrConcurrentUpdate) {
		return "version", true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001":
			return "serialization", true
		case "40P01":
			return "deadlock", true
		}
	}
	return "", false
}

func Classify(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil {
		return err
	}
	if isTransient(err) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}

var transientCodes = map[string]bool{
	"55P03": true,
	"57014": true,
	"53300": true,
	"57P01": true,
	"57P03": true,
}

func isTransient(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return transientCodes[pgErr.Code] || strings.HasPrefix(pgErr.Code, "08")
	}
	var connectErr *pgconn.ConnectError
	return errors.As(err, &connectErr) || pgconn.Timeout(err) || pgconn.SafeToRetry(err)
}

func backoff(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt)*10*time.Millisecond + time.Duration(rand.IntN(10))*time.Millisecond
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func isForeignKeyViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == constraint
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
