package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
)

type LedgerRepo struct {
	q querier
}

type LedgerCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

const ledgerColumns = `id, wallet_id, transaction_id, direction, amount, currency, balance_before, balance_after, created_at`

func (r LedgerRepo) Insert(ctx context.Context, e domain.LedgerEntry) error {
	_, err := r.q.Exec(ctx,
		`INSERT INTO wallet_ledger_entries (`+ledgerColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.ID(), e.WalletID(), e.TransactionID(), e.Direction(), e.Amount().MinorUnits(), e.Amount().Currency().String(),
		e.BalanceBefore().MinorUnits(), e.BalanceAfter().MinorUnits(), e.CreatedAt())
	return err
}

func (r LedgerRepo) ListByWallet(ctx context.Context, walletID uuid.UUID, after *LedgerCursor, limit int) ([]domain.LedgerEntry, error) {
	var rows pgx.Rows
	var err error
	if after == nil {
		rows, err = r.q.Query(ctx,
			`SELECT `+ledgerColumns+` FROM wallet_ledger_entries
			 WHERE wallet_id = $1 ORDER BY created_at, id LIMIT $2`, walletID, limit)
	} else {
		rows, err = r.q.Query(ctx,
			`SELECT `+ledgerColumns+` FROM wallet_ledger_entries
			 WHERE wallet_id = $1 AND (created_at, id) > ($2, $3) ORDER BY created_at, id LIMIT $4`,
			walletID, after.CreatedAt, after.ID, limit)
	}
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanLedgerEntry)
}

func scanLedgerEntry(row pgx.CollectableRow) (domain.LedgerEntry, error) {
	var (
		id, walletID, transactionID       uuid.UUID
		direction, currency               string
		amount, balanceBefore, balanceAft int64
		createdAt                         time.Time
	)
	if err := row.Scan(&id, &walletID, &transactionID, &direction, &amount, &currency, &balanceBefore, &balanceAft, &createdAt); err != nil {
		return domain.LedgerEntry{}, err
	}
	money := func(minor int64) (domain.Money, error) { return toMoney(minor, currency) }
	amountM, err := money(amount)
	if err != nil {
		return domain.LedgerEntry{}, err
	}
	beforeM, err := money(balanceBefore)
	if err != nil {
		return domain.LedgerEntry{}, err
	}
	afterM, err := money(balanceAft)
	if err != nil {
		return domain.LedgerEntry{}, err
	}
	return domain.RehydrateLedgerEntry(id, walletID, transactionID, domain.Direction(direction), amountM, beforeM, afterM, createdAt)
}

var ErrSumOutOfRange = errors.New("ledger sum out of int64 range")

func (r LedgerRepo) Totals(ctx context.Context, walletID uuid.UUID) (net int64, entries int, err error) {
	var sum *int64
	err = r.q.QueryRow(ctx, `
		SELECT CASE WHEN total BETWEEN -9223372036854775808 AND 9223372036854775807 THEN total::bigint END, entries
		FROM (
			SELECT COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount::numeric ELSE -amount::numeric END), 0) AS total,
			       count(*) AS entries
			FROM wallet_ledger_entries WHERE wallet_id = $1
		) totals`, walletID).Scan(&sum, &entries)
	if err != nil {
		return 0, 0, err
	}
	if sum == nil {
		return 0, 0, ErrSumOutOfRange
	}
	return *sum, entries, nil
}
