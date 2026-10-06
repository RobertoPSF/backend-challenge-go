//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx/fxtest"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/postgres"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
	"github.com/RobertoPSF/backend-challenge-go/test/testinfra"
)

var silentLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type storeEnv struct {
	store *store.Store
	pool  *pgxpool.Pool
	reg   *prometheus.Registry
}

func newStoreEnv(t *testing.T, lockTimeout time.Duration) storeEnv {
	t.Helper()
	pg := testinfra.StartPostgres(t)
	cfg := config.Config{Database: config.Database{
		URL: pg.AppURL, MaxConns: 10, LockTimeout: lockTimeout, StatementTimeout: 10 * time.Second,
	}}
	lc := fxtest.NewLifecycle(t)
	pool, err := postgres.NewPool(lc, cfg, silentLog)
	if err != nil {
		t.Fatal(err)
	}
	lc.RequireStart()
	t.Cleanup(lc.RequireStop)

	reg := prometheus.NewRegistry()
	return storeEnv{store: store.New(pool, reg, silentLog), pool: pool, reg: reg}
}

func brl(t *testing.T, amount string) domain.Money {
	t.Helper()
	m, err := domain.ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var ctxEvent = domain.EventContext{CorrelationID: "corr-test", OccurredAt: time.Now()}

func openWallet(t *testing.T, env storeEnv, initial string) domain.WalletOpening {
	t.Helper()
	op, err := domain.OpenWallet(uuid.New(), brl(t, initial), ctxEvent)
	if err != nil {
		t.Fatal(err)
	}
	err = env.store.InTx(context.Background(), func(r *store.Repos) error {
		if err := r.Wallets.Insert(context.Background(), op.Wallet); err != nil {
			return err
		}
		if op.Transaction == nil {
			return nil
		}
		if err := r.Transactions.Insert(context.Background(), op.Transaction, ctxEvent.CorrelationID); err != nil {
			return err
		}
		if err := r.Ledger.Insert(context.Background(), *op.LedgerEntry); err != nil {
			return err
		}
		return r.Outbox.Insert(context.Background(), op.Events...)
	})
	if err != nil {
		t.Fatalf("persist opening: %v", err)
	}
	return op
}

func TestStore(t *testing.T) {
	env := newStoreEnv(t, 500*time.Millisecond)
	ctx := context.Background()

	t.Run("opening round trip", func(t *testing.T) {
		op := openWallet(t, env, "1000.00")
		r := env.store.Read()

		w, err := r.Wallets.Get(ctx, op.Wallet.ID())
		if err != nil {
			t.Fatal(err)
		}
		if w.Balance().String() != "1000.00" || w.Version() != 1 || w.PlayerID() != op.Wallet.PlayerID() ||
			!w.CreatedAt().Equal(op.Wallet.CreatedAt()) {
			t.Errorf("wallet = %s v%d", w.Balance(), w.Version())
		}

		tx, err := r.Transactions.Get(ctx, op.Transaction.ID())
		if err != nil {
			t.Fatal(err)
		}
		if got, want := tx.Snapshot(), op.Transaction.Snapshot(); got.Kind != domain.KindOpening ||
			got.Status != domain.StatusProcessed || got.External != nil || !got.BalanceAfter.Equal(*want.BalanceAfter) ||
			!got.ProcessedAt.Equal(*want.ProcessedAt) {
			t.Errorf("opening tx = %+v", got)
		}

		entries, err := r.Ledger.ListByWallet(ctx, op.Wallet.ID(), nil, 10)
		if err != nil || len(entries) != 1 || entries[0].ID() != op.LedgerEntry.ID() {
			t.Fatalf("ledger = %v, %v", entries, err)
		}

		var count int
		var payload []byte
		_ = env.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1`, op.Wallet.ID()).Scan(&count)
		_ = env.pool.QueryRow(ctx, `SELECT payload FROM outbox_events WHERE event_id = $1`, op.Events[1].Header().EventID).Scan(&payload)
		if count != 2 {
			t.Errorf("outbox events = %d, want 2", count)
		}
		var event map[string]any
		_ = json.Unmarshal(payload, &event)
		if event["eventType"] != domain.EventWalletBalanceChanged || event["data"].(map[string]any)["walletVersion"] != float64(1) {
			t.Errorf("outbox payload = %s", payload)
		}
	})

	t.Run("external transaction round trip keeps empty reference as NULL", func(t *testing.T) {
		op := openWallet(t, env, "100.00")
		ext := domain.ExternalDetails{
			ProviderID: "provider-a", ExternalTransactionID: uuid.NewString(), IdempotencyKey: "k-" + uuid.NewString(),
			PayloadHash: "hash", RoundID: "round-1", GameID: "game-1",
		}
		bet, err := domain.NewExternalTransaction(domain.KindBet, op.Wallet.ID(), op.Wallet.PlayerID(), brl(t, "80.00"), ext, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := bet.MarkRejected("INSUFFICIENT_FUNDS", brl(t, "20.00"), time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := env.store.InTx(ctx, func(r *store.Repos) error { return r.Transactions.Insert(ctx, bet, "corr") }); err != nil {
			t.Fatal(err)
		}

		got, err := env.store.Read().Transactions.Get(ctx, bet.ID())
		if err != nil {
			t.Fatal(err)
		}
		s := got.Snapshot()
		if *s.External != ext || s.FailureCode != "INSUFFICIENT_FUNDS" || s.BalanceAfter.String() != "20.00" {
			t.Errorf("round trip = %+v %+v", s, s.External)
		}
		var ref *string
		_ = env.pool.QueryRow(ctx, `SELECT reference_external_transaction_id FROM wager_transactions WHERE id = $1`, bet.ID()).Scan(&ref)
		if ref != nil {
			t.Errorf("empty reference stored as %q, want NULL", *ref)
		}
	})

	t.Run("duplicate wallet maps to a domain conflict", func(t *testing.T) {
		op := openWallet(t, env, "0.00")
		dup, _ := domain.OpenWallet(op.Wallet.PlayerID(), domain.Zero(domain.BRL), ctxEvent)
		err := env.store.InTx(ctx, func(r *store.Repos) error { return r.Wallets.Insert(ctx, dup.Wallet) })
		if !errors.Is(err, domain.ErrWalletAlreadyExists) {
			t.Fatalf("error = %v, want ErrWalletAlreadyExists", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		if _, err := env.store.Read().Wallets.Get(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("wallet error = %v", err)
		}
		if _, err := env.store.Read().Transactions.Get(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("transaction error = %v", err)
		}
	})

	t.Run("error inside the transaction rolls everything back", func(t *testing.T) {
		op, _ := domain.OpenWallet(uuid.New(), brl(t, "10.00"), ctxEvent)
		boom := errors.New("boom")
		err := env.store.InTx(ctx, func(r *store.Repos) error {
			_ = r.Wallets.Insert(ctx, op.Wallet)
			_ = r.Transactions.Insert(ctx, op.Transaction, "corr")
			_ = r.Ledger.Insert(ctx, *op.LedgerEntry)
			_ = r.Outbox.Insert(ctx, op.Events...)
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("error = %v, want boom", err)
		}
		var rows int
		_ = env.pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM wallets WHERE id = $1) + (SELECT count(*) FROM wager_transactions WHERE wallet_id = $1) +
			(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1) + (SELECT count(*) FROM outbox_events WHERE aggregate_id = $1)`,
			op.Wallet.ID()).Scan(&rows)
		if rows != 0 {
			t.Fatalf("rows left after rollback = %d", rows)
		}
	})

	t.Run("stale version is a concurrent update", func(t *testing.T) {
		op := openWallet(t, env, "100.00")
		w, _ := env.store.Read().Wallets.Get(ctx, op.Wallet.ID())
		_, _ = w.Debit(uuid.New(), brl(t, "1.00"), time.Now())

		err := env.store.Read().Wallets.UpdateBalance(ctx, w, w.Version()+5)
		if !errors.Is(err, store.ErrConcurrentUpdate) {
			t.Fatalf("error = %v, want ErrConcurrentUpdate", err)
		}
		if err := env.store.Read().Wallets.UpdateBalance(ctx, w, w.Version()-1); err != nil {
			t.Fatalf("update with the expected version: %v", err)
		}
	})

	t.Run("concurrent update is retried and then succeeds", func(t *testing.T) {
		before := metricValue(t, env.reg, "wallet_concurrency_conflicts_total", "reason", "version")
		var calls atomic.Int32
		err := env.store.InTx(ctx, func(r *store.Repos) error {
			if calls.Add(1) == 1 {
				return store.ErrConcurrentUpdate
			}
			return nil
		})
		if err != nil || calls.Load() != 2 {
			t.Fatalf("err = %v, calls = %d; want success on the 2nd attempt", err, calls.Load())
		}
		if got := metricValue(t, env.reg, "wallet_concurrency_conflicts_total", "reason", "version") - before; got != 1 {
			t.Errorf("conflict metric increased by %v, want 1", got)
		}
	})

	t.Run("retries are bounded", func(t *testing.T) {
		var calls atomic.Int32
		err := env.store.InTx(ctx, func(r *store.Repos) error {
			calls.Add(1)
			return store.ErrConcurrentUpdate
		})
		if !errors.Is(err, store.ErrConcurrentUpdate) || calls.Load() != 3 {
			t.Fatalf("err = %v, calls = %d; want ErrConcurrentUpdate after 3 attempts", err, calls.Load())
		}
	})

	t.Run("lock wait beyond lock_timeout is a transient error", func(t *testing.T) {
		op := openWallet(t, env, "100.00")
		locked, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- env.store.InTx(ctx, func(r *store.Repos) error {
				if _, err := r.Wallets.GetForUpdate(ctx, op.Wallet.ID()); err != nil {
					return err
				}
				close(locked)
				<-release
				return nil
			})
		}()
		<-locked

		start := time.Now()
		err := env.store.InTx(ctx, func(r *store.Repos) error {
			_, err := r.Wallets.GetForUpdate(ctx, op.Wallet.ID())
			return err
		})
		waited := time.Since(start)
		close(release)

		if !errors.Is(err, store.ErrUnavailable) {
			t.Fatalf("error = %v, want ErrUnavailable", err)
		}
		if waited < 400*time.Millisecond || waited > 3*time.Second {
			t.Errorf("waited %s, want about the 500ms lock_timeout", waited)
		}
		if err := <-done; err != nil {
			t.Fatalf("lock holder: %v", err)
		}
	})

	t.Run("other wallets are not blocked by a wallet lock", func(t *testing.T) {
		locked, other := openWallet(t, env, "100.00"), openWallet(t, env, "100.00")
		hold, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- env.store.InTx(ctx, func(r *store.Repos) error {
				if _, err := r.Wallets.GetForUpdate(ctx, locked.Wallet.ID()); err != nil {
					return err
				}
				close(hold)
				<-release
				return nil
			})
		}()
		<-hold

		start := time.Now()
		err := env.store.InTx(ctx, func(r *store.Repos) error {
			_, err := r.Wallets.GetForUpdate(ctx, other.Wallet.ID())
			return err
		})
		close(release)
		if err != nil || time.Since(start) > 200*time.Millisecond {
			t.Fatalf("locking another wallet took %s, err = %v", time.Since(start), err)
		}
		_ = <-done
	})

	t.Run("ledger pagination is stable", func(t *testing.T) {
		op := openWallet(t, env, "100.00")
		err := env.store.InTx(ctx, func(r *store.Repos) error {
			w, err := r.Wallets.GetForUpdate(ctx, op.Wallet.ID())
			if err != nil {
				return err
			}
			for range 4 {
				ext := domain.ExternalDetails{ProviderID: "p", ExternalTransactionID: uuid.NewString(), IdempotencyKey: uuid.NewString(),
					PayloadHash: "h", RoundID: "r", GameID: "g"}
				tx, err := domain.NewExternalTransaction(domain.KindBet, w.ID(), w.PlayerID(), brl(t, "1.00"), ext, time.Now())
				if err != nil {
					return err
				}
				expected := w.Version()
				entry, err := w.Debit(tx.ID(), tx.Money(), time.Now())
				if err != nil {
					return err
				}
				if err := tx.MarkProcessed(w.Balance(), nil, time.Now()); err != nil {
					return err
				}
				if err := r.Transactions.Insert(ctx, tx, "corr"); err != nil {
					return err
				}
				if err := r.Ledger.Insert(ctx, entry); err != nil {
					return err
				}
				if err := r.Wallets.UpdateBalance(ctx, w, expected); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		var all []domain.LedgerEntry
		var cursor *store.LedgerCursor
		for {
			page, err := env.store.Read().Ledger.ListByWallet(ctx, op.Wallet.ID(), cursor, 2)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, page...)
			if len(page) < 2 {
				break
			}
			last := page[len(page)-1]
			cursor = &store.LedgerCursor{CreatedAt: last.CreatedAt(), ID: last.ID()}
		}
		if len(all) != 5 {
			t.Fatalf("entries = %d, want 5 (opening + 4 bets)", len(all))
		}
		for i := 1; i < len(all); i++ {
			if !all[i].BalanceBefore().Equal(all[i-1].BalanceAfter()) {
				t.Errorf("entry %d does not chain: before %s, previous after %s", i, all[i].BalanceBefore(), all[i-1].BalanceAfter())
			}
		}
		w, _ := env.store.Read().Wallets.Get(ctx, op.Wallet.ID())
		if w.Balance().String() != "96.00" || w.Version() != 5 || !all[4].BalanceAfter().Equal(w.Balance()) {
			t.Errorf("wallet = %s v%d", w.Balance(), w.Version())
		}
	})
}

func metricValue(t *testing.T, reg *prometheus.Registry, name, label, value string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
