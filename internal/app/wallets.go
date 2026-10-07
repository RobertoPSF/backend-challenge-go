package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
)

var Module = fx.Module("app", fx.Provide(NewWallets, NewWagers, NewReconciler))

var (
	ErrNotFound    = errors.New("resource not found")
	ErrUnavailable = store.ErrUnavailable
)

type LedgerCursor = store.LedgerCursor

type LedgerPage struct {
	Entries []domain.LedgerEntry
	Next    *LedgerCursor
}

type Wallets struct {
	store *store.Store
	now   func() time.Time
}

func NewWallets(st *store.Store) *Wallets {
	return &Wallets{store: st, now: time.Now}
}

func (s *Wallets) Open(ctx context.Context, playerID uuid.UUID, initialBalance domain.Money, correlationID string) (*domain.Wallet, error) {
	opening, err := domain.OpenWallet(playerID, initialBalance, domain.EventContext{
		CorrelationID: correlationID,
		OccurredAt:    s.now(),
	})
	if err != nil {
		return nil, err
	}

	err = s.store.InTx(ctx, func(ctx context.Context, r *store.Repos) error {
		if err := r.Wallets.Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if opening.Transaction == nil {
			return nil
		}
		if err := r.Transactions.Insert(ctx, opening.Transaction, correlationID); err != nil {
			return err
		}
		if err := r.Ledger.Insert(ctx, *opening.LedgerEntry); err != nil {
			return err
		}
		return r.Outbox.Insert(ctx, opening.Events...)
	})
	if err != nil {
		return nil, err
	}
	return opening.Wallet, nil
}

func (s *Wallets) Get(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	var w *domain.Wallet
	err := s.store.Query(ctx, func(ctx context.Context, r *store.Repos) (err error) {
		w, err = r.Wallets.Get(ctx, id)
		return err
	})
	return w, translate(err)
}

func (s *Wallets) Ledger(ctx context.Context, walletID uuid.UUID, after *LedgerCursor, limit int) (LedgerPage, error) {
	var entries []domain.LedgerEntry
	err := s.store.Query(ctx, func(ctx context.Context, r *store.Repos) (err error) {
		if _, err := r.Wallets.Get(ctx, walletID); err != nil {
			return err
		}
		entries, err = r.Ledger.ListByWallet(ctx, walletID, after, limit+1)
		return err
	})
	if err != nil {
		return LedgerPage{}, translate(err)
	}

	page := LedgerPage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		last := page.Entries[limit-1]
		page.Next = &LedgerCursor{CreatedAt: last.CreatedAt(), ID: last.ID()}
	}
	return page, nil
}

func translate(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
