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
	s := t.Snapshot()
	origin := "INTERNAL"
	var ext domain.ExternalDetails
	if s.External != nil {
		origin = "EXTERNAL"
		ext = *s.External
	}
	balanceAfter, balanceCurrency := balanceColumns(s.BalanceAfter)

	_, err := r.q.Exec(ctx, `INSERT INTO wager_transactions (origin, correlation_id, `+transactionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)`,
		origin, nullIfEmpty(correlationID),
		s.ID, s.Kind, s.Status, s.WalletID, s.PlayerID, s.Money.Currency().String(), s.Money.MinorUnits(),
		nullIfEmpty(ext.ProviderID), nullIfEmpty(ext.ExternalTransactionID), nullIfEmpty(ext.IdempotencyKey),
		nullIfEmpty(ext.PayloadHash), nullIfEmpty(ext.RoundID), nullIfEmpty(ext.GameID),
		nullIfEmpty(ext.ReferenceExternalTransactionID), s.ReferenceTransactionID, nullIfEmpty(string(s.FailureCode)),
		balanceAfter, balanceCurrency, s.CreatedAt, s.UpdatedAt, s.ProcessedAt)
	return err
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

func scanTransaction(row pgx.Row) (*domain.WagerTransaction, error) {
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
	err := row.Scan(&s.ID, &s.Kind, &s.Status, &s.WalletID, &s.PlayerID, &currency, &amount,
		&providerID, &externalID, &idempotencyKey, &hash, &roundID, &gameID,
		&referenceExternalID, &s.ReferenceTransactionID, &failure,
		&balanceAfter, &balanceCurrency, &s.CreatedAt, &s.UpdatedAt, &processedAt)
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
