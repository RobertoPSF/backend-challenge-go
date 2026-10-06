package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
)

type WagerCommand struct {
	Request       domain.WagerRequest
	CorrelationID string
	CausationID   string
}

type WagerResult struct {
	Transaction *domain.WagerTransaction
	Replay      bool
}

type Wagers struct {
	store *store.Store
	now   func() time.Time
}

func NewWagers(st *store.Store) *Wagers {
	return &Wagers{store: st, now: time.Now}
}

func (s *Wagers) Process(ctx context.Context, cmd WagerCommand) (WagerResult, error) {
	if _, err := s.newTransaction(cmd.Request); err != nil {
		return WagerResult{}, err
	}

	var result WagerResult
	err := s.store.InTx(ctx, func(r *store.Repos) error {
		var err error
		result, err = s.process(ctx, r, cmd)
		return err
	})
	if err != nil {
		return WagerResult{}, err
	}
	return result, nil
}

func (s *Wagers) process(ctx context.Context, r *store.Repos, cmd WagerCommand) (WagerResult, error) {
	tx, err := s.newTransaction(cmd.Request)
	if err != nil {
		return WagerResult{}, err
	}

	inserted, err := r.Transactions.InsertIfAbsent(ctx, tx, cmd.CorrelationID)
	if err != nil {
		return WagerResult{}, err
	}
	if !inserted {
		return s.replay(ctx, r, cmd.Request)
	}

	wallet, err := r.Wallets.GetForUpdate(ctx, tx.WalletID())
	if err != nil {
		return WagerResult{}, err
	}
	expectedVersion := wallet.Version()
	now := s.now()
	eventCtx := domain.EventContext{CorrelationID: cmd.CorrelationID, CausationID: cmd.CausationID, OccurredAt: now}

	var events []domain.Event
	entry, err := domain.ApplyToWallet(wallet, tx, now)
	switch {
	case isBusinessRejection(err):
		if err := tx.MarkRejected(failureCode(err), wallet.Balance(), now); err != nil {
			return WagerResult{}, err
		}
		rejected, err := domain.NewWagerTransactionRejected(tx, eventCtx)
		if err != nil {
			return WagerResult{}, err
		}
		events = append(events, rejected)

	case err != nil:
		return WagerResult{}, err

	default:
		if entry != nil {
			if err := r.Ledger.Insert(ctx, *entry); err != nil {
				return WagerResult{}, err
			}
			if err := r.Wallets.UpdateBalance(ctx, wallet, expectedVersion); err != nil {
				return WagerResult{}, err
			}
		}
		if err := tx.MarkProcessed(wallet.Balance(), nil, now); err != nil {
			return WagerResult{}, err
		}
		processed, err := domain.NewWagerTransactionProcessed(tx, eventCtx)
		if err != nil {
			return WagerResult{}, err
		}
		events = append(events, processed)
		if entry != nil {
			changed, err := domain.NewWalletBalanceChanged(*entry, wallet.Version(), eventCtx)
			if err != nil {
				return WagerResult{}, err
			}
			events = append(events, changed)
		}
	}

	if err := r.Transactions.Update(ctx, tx); err != nil {
		return WagerResult{}, err
	}
	if err := r.Outbox.Insert(ctx, events...); err != nil {
		return WagerResult{}, err
	}
	return WagerResult{Transaction: tx}, nil
}

func (s *Wagers) newTransaction(req domain.WagerRequest) (*domain.WagerTransaction, error) {
	return domain.NewExternalTransaction(req.Kind, req.WalletID, req.PlayerID, req.Money, req.ExternalDetails(), s.now())
}

func (s *Wagers) replay(ctx context.Context, r *store.Repos, req domain.WagerRequest) (WagerResult, error) {
	existing, err := r.Transactions.FindByIdempotencyKey(ctx, req.ProviderID, req.IdempotencyKey)
	if errors.Is(err, store.ErrNotFound) {
		if _, err := r.Transactions.FindByExternalID(ctx, req.ProviderID, req.ExternalTransactionID); err != nil {
			return WagerResult{}, err
		}
		return WagerResult{}, fmt.Errorf("%w: %s", domain.ErrExternalTransactionConflict, req.ExternalTransactionID)
	}
	if err != nil {
		return WagerResult{}, err
	}
	if existing.Snapshot().External.PayloadHash != req.PayloadHash() {
		return WagerResult{}, fmt.Errorf("%w: %s", domain.ErrIdempotencyKeyConflict, req.IdempotencyKey)
	}
	return WagerResult{Transaction: existing, Replay: true}, nil
}

func isBusinessRejection(err error) bool {
	var de *domain.DomainError
	return errors.As(err, &de) && de.Kind == domain.KindBusiness
}

func failureCode(err error) domain.FailureCode {
	var de *domain.DomainError
	errors.As(err, &de)
	return de.Code
}
