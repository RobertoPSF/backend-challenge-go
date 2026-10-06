package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
)

type WalletRepo struct {
	q querier
}

const walletColumns = `id, player_id, currency, balance, version, created_at, updated_at`

func (r WalletRepo) Insert(ctx context.Context, w *domain.Wallet) error {
	_, err := r.q.Exec(ctx,
		`INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		w.ID(), w.PlayerID(), w.Currency().String(), w.Balance().MinorUnits(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	if isUniqueViolation(err, "wallets_player_currency_uk") {
		return domain.ErrWalletAlreadyExists
	}
	return err
}

func (r WalletRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	return scanWallet(r.q.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id))
}

func (r WalletRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	return scanWallet(r.q.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id))
}

func (r WalletRepo) UpdateBalance(ctx context.Context, w *domain.Wallet, expectedVersion int64) error {
	tag, err := r.q.Exec(ctx,
		`UPDATE wallets SET balance = $1, version = $2, updated_at = $3 WHERE id = $4 AND version = $5`,
		w.Balance().MinorUnits(), w.Version(), w.UpdatedAt(), w.ID(), expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}

func scanWallet(row pgx.Row) (*domain.Wallet, error) {
	var (
		id, playerID         uuid.UUID
		currencyCode         string
		balance, version     int64
		createdAt, updatedAt time.Time
	)
	if err := row.Scan(&id, &playerID, &currencyCode, &balance, &version, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	money, err := toMoney(balance, currencyCode)
	if err != nil {
		return nil, err
	}
	return domain.RehydrateWallet(id, playerID, money, version, createdAt, updatedAt)
}

func toMoney(minor int64, currencyCode string) (domain.Money, error) {
	currency, err := domain.ParseCurrency(currencyCode)
	if err != nil {
		return domain.Money{}, err
	}
	return domain.NewMoney(minor, currency)
}
