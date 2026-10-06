package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
)

type TransactionRepo struct {
	q querier
}

const transactionColumns = `id, kind, status, wallet_id, player_id, currency, amount,
	provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
	reference_external_transaction_id, reference_transaction_id, failure_code,
	balance_after, balance_currency, created_at, updated_at, processed_at`

func (r TransactionRepo) Insert(ctx context.Context, t *domain.WagerTransaction, correlationID string) error {
	_, err := r.insert(ctx, t, correlationID, "")
	return err
}

func (r TransactionRepo) InsertIfAbsent(ctx context.Context, t *domain.WagerTransaction, correlationID string) (bool, error) {
	return r.insert(ctx, t, correlationID, "ON CONFLICT DO NOTHING")
}

func (r TransactionRepo) insert(ctx context.Context, t *domain.WagerTransaction, correlationID, onConflict string) (bool, error) {
	s := t.Snapshot()
	origin := "INTERNAL"
	var ext domain.ExternalDetails
	if s.External != nil {
		origin = "EXTERNAL"
		ext = *s.External
	}
	balanceAfter, balanceCurrency := balanceColumns(s.BalanceAfter)

	tag, err := r.q.Exec(ctx, `INSERT INTO wager_transactions (origin, correlation_id, `+transactionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23) `+onConflict,
		origin, nullIfEmpty(correlationID),
		s.ID, s.Kind, s.Status, s.WalletID, s.PlayerID, s.Money.Currency().String(), s.Money.MinorUnits(),
		nullIfEmpty(ext.ProviderID), nullIfEmpty(ext.ExternalTransactionID), nullIfEmpty(ext.IdempotencyKey),
		nullIfEmpty(ext.PayloadHash), nullIfEmpty(ext.RoundID), nullIfEmpty(ext.GameID),
		nullIfEmpty(ext.ReferenceExternalTransactionID), s.ReferenceTransactionID, nullIfEmpty(string(s.FailureCode)),
		balanceAfter, balanceCurrency, s.CreatedAt, s.UpdatedAt, s.ProcessedAt)
	if isForeignKeyViolation(err, "wager_transactions_wallet_id_fkey") {
		return false, domain.ErrWalletNotFound
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r TransactionRepo) Update(ctx context.Context, t *domain.WagerTransaction) error {
	s := t.Snapshot()
	balanceAfter, balanceCurrency := balanceColumns(s.BalanceAfter)
	tag, err := r.q.Exec(ctx, `UPDATE wager_transactions
		SET status = $1, failure_code = $2, balance_after = $3, balance_currency = $4,
		    reference_transaction_id = $5, updated_at = $6, processed_at = $7
		WHERE id = $8`,
		s.Status, nullIfEmpty(string(s.FailureCode)), balanceAfter, balanceCurrency,
		s.ReferenceTransactionID, s.UpdatedAt, s.ProcessedAt, s.ID)
	if isUniqueViolation(err, "wt_single_successful_reversal_uk") {
		return ErrConcurrentUpdate
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r TransactionRepo) SchedulePending(ctx context.Context, id uuid.UUID, nextAttemptAt, expiresAt time.Time) error {
	_, err := r.q.Exec(ctx,
		`UPDATE wager_transactions SET next_attempt_at = $1, expires_at = $2 WHERE id = $3`,
		nextAttemptAt, expiresAt, id)
	return err
}

func (r TransactionRepo) HasSuccessfulReversal(ctx context.Context, referenceID uuid.UUID) (bool, error) {
	var exists bool
	err := r.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wager_transactions
		WHERE reference_transaction_id = $1 AND status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK'))`,
		referenceID).Scan(&exists)
	return exists, err
}

type TransactionView struct {
	Transaction   *domain.WagerTransaction
	Attempts      int
	NextAttemptAt *time.Time
	ExpiresAt     *time.Time
}

const viewColumns = transactionColumns + `, attempts, next_attempt_at, expires_at`

func (r TransactionRepo) GetView(ctx context.Context, id uuid.UUID) (TransactionView, error) {
	return scanView(r.q.QueryRow(ctx, `SELECT `+viewColumns+` FROM wager_transactions WHERE id = $1`, id))
}

func (r TransactionRepo) FindViewByExternalID(ctx context.Context, providerID, externalID string) (TransactionView, error) {
	return scanView(r.q.QueryRow(ctx, `SELECT `+viewColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND external_transaction_id = $2`, providerID, externalID))
}

func scanView(row pgx.Row) (TransactionView, error) {
	var v TransactionView
	tx, err := scanTransaction(row, &v.Attempts, &v.NextAttemptAt, &v.ExpiresAt)
	if err != nil {
		return TransactionView{}, err
	}
	v.Transaction = tx
	return v, nil
}

func (r TransactionRepo) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*domain.WagerTransaction, error) {
	return scanTransaction(r.q.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND idempotency_key = $2`, providerID, key))
}

func (r TransactionRepo) FindByExternalID(ctx context.Context, providerID, externalID string) (*domain.WagerTransaction, error) {
	return scanTransaction(r.q.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND external_transaction_id = $2`, providerID, externalID))
}

func (r TransactionRepo) Get(ctx context.Context, id uuid.UUID) (*domain.WagerTransaction, error) {
	return scanTransaction(r.q.QueryRow(ctx, `SELECT `+transactionColumns+` FROM wager_transactions WHERE id = $1`, id))
}

func balanceColumns(balance *domain.Money) (*int64, *string) {
	if balance == nil {
		return nil, nil
	}
	minor, currency := balance.MinorUnits(), balance.Currency().String()
	return &minor, &currency
}

func scanTransaction(row pgx.Row, extra ...any) (*domain.WagerTransaction, error) {
	var (
		s                                             domain.WagerTransactionSnapshot
		currency                                      string
		amount                                        int64
		providerID, externalID, idempotencyKey, hash  *string
		roundID, gameID, referenceExternalID, failure *string
		balanceAfter                                  *int64
		balanceCurrency                               *string
		processedAt                                   *time.Time
	)
	dest := []any{&s.ID, &s.Kind, &s.Status, &s.WalletID, &s.PlayerID, &currency, &amount,
		&providerID, &externalID, &idempotencyKey, &hash, &roundID, &gameID,
		&referenceExternalID, &s.ReferenceTransactionID, &failure,
		&balanceAfter, &balanceCurrency, &s.CreatedAt, &s.UpdatedAt, &processedAt}
	err := row.Scan(append(dest, extra...)...)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if s.Money, err = toMoney(amount, currency); err != nil {
		return nil, err
	}
	if balanceAfter != nil {
		balance, err := toMoney(*balanceAfter, deref(balanceCurrency))
		if err != nil {
			return nil, err
		}
		s.BalanceAfter = &balance
	}
	if s.Kind != domain.KindOpening {
		s.External = &domain.ExternalDetails{
			ProviderID:                     deref(providerID),
			ExternalTransactionID:          deref(externalID),
			IdempotencyKey:                 deref(idempotencyKey),
			PayloadHash:                    deref(hash),
			RoundID:                        deref(roundID),
			GameID:                         deref(gameID),
			ReferenceExternalTransactionID: deref(referenceExternalID),
		}
	}
	s.FailureCode = domain.FailureCode(deref(failure))
	s.ProcessedAt = processedAt
	return domain.RehydrateWagerTransaction(s)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
