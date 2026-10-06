package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
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
	store   *store.Store
	pending config.Pending
	now     func() time.Time
}

func NewWagers(st *store.Store, cfg config.Config) *Wagers {
	return &Wagers{store: st, pending: cfg.Pending, now: time.Now}
}

func (s *Wagers) Process(ctx context.Context, cmd WagerCommand) (WagerResult, error) {
	if _, err := s.newTransaction(cmd.Request); err != nil {
		return WagerResult{}, err
	}

	var result WagerResult
	err := s.store.InTx(ctx, func(r *store.Repos) error {
		tx, err := s.newTransaction(cmd.Request)
		if err != nil {
			return err
		}
		inserted, err := r.Transactions.InsertIfAbsent(ctx, tx, cmd.CorrelationID)
		if err != nil {
			return err
		}
		if !inserted {
			result, err = s.replay(ctx, r, cmd.Request)
			return err
		}
		result = WagerResult{Transaction: tx}
		return s.settle(ctx, r, tx, s.eventContext(cmd.CorrelationID, cmd.CausationID))
	})
	if err != nil {
		return WagerResult{}, err
	}
	return result, nil
}

func (s *Wagers) settle(ctx context.Context, r *store.Repos, tx *domain.WagerTransaction, eventCtx domain.EventContext) error {
	var ref *domain.WagerTransaction
	if tx.HasReference() {
		var err error
		ref, err = s.findReference(ctx, r, tx)
		if err != nil {
			return err
		}
		if ref == nil || !ref.Status().IsTerminal() {
			return s.waitForReference(ctx, r, tx, eventCtx)
		}
	}

	wallet, err := r.Wallets.GetForUpdate(ctx, tx.WalletID())
	if err != nil {
		return err
	}
	alreadyReversed := false
	if tx.Kind().IsReversal() && ref.Status() == domain.StatusProcessed {
		if alreadyReversed, err = r.Transactions.HasSuccessfulReversal(ctx, ref.ID()); err != nil {
			return err
		}
	}

	expectedVersion := wallet.Version()
	entry, err := domain.ApplyToWallet(wallet, tx, ref, alreadyReversed, eventCtx.OccurredAt)
	if isBusinessRejection(err) {
		return s.reject(ctx, r, tx, failureCode(err), wallet, eventCtx)
	}
	if err != nil {
		return err
	}

	events := []domain.Event{}
	if entry != nil {
		if err := r.Ledger.Insert(ctx, *entry); err != nil {
			return err
		}
		if err := r.Wallets.UpdateBalance(ctx, wallet, expectedVersion); err != nil {
			return err
		}
	}
	var refID *uuid.UUID
	if ref != nil {
		id := ref.ID()
		refID = &id
	}
	if err := tx.MarkProcessed(wallet.Balance(), refID, eventCtx.OccurredAt); err != nil {
		return err
	}
	processed, err := domain.NewWagerTransactionProcessed(tx, eventCtx)
	if err != nil {
		return err
	}
	events = append(events, processed)
	if entry != nil {
		changed, err := domain.NewWalletBalanceChanged(*entry, wallet.Version(), eventCtx)
		if err != nil {
			return err
		}
		events = append(events, changed)
	}
	return s.save(ctx, r, tx, events...)
}

func (s *Wagers) findReference(ctx context.Context, r *store.Repos, tx *domain.WagerTransaction) (*domain.WagerTransaction, error) {
	ext := tx.Snapshot().External
	ref, err := r.Transactions.FindByExternalID(ctx, ext.ProviderID, ext.ReferenceExternalTransactionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return ref, err
}

func (s *Wagers) waitForReference(ctx context.Context, r *store.Repos, tx *domain.WagerTransaction, eventCtx domain.EventContext) error {
	if tx.Status() == domain.StatusPendingReference {
		return nil
	}
	now := eventCtx.OccurredAt
	if err := tx.MarkPendingReference(now); err != nil {
		return err
	}
	pending, err := domain.NewWagerTransactionPendingReference(tx, eventCtx)
	if err != nil {
		return err
	}
	if err := s.save(ctx, r, tx, pending); err != nil {
		return err
	}
	return r.Transactions.SchedulePending(ctx, tx.ID(), now.Add(s.pending.BaseBackoff), now.Add(s.pending.TTL))
}

func (s *Wagers) reject(ctx context.Context, r *store.Repos, tx *domain.WagerTransaction, code domain.FailureCode, wallet *domain.Wallet, eventCtx domain.EventContext) error {
	if err := tx.MarkRejected(code, wallet.Balance(), eventCtx.OccurredAt); err != nil {
		return err
	}
	rejected, err := domain.NewWagerTransactionRejected(tx, eventCtx)
	if err != nil {
		return err
	}
	return s.save(ctx, r, tx, rejected)
}

func (s *Wagers) save(ctx context.Context, r *store.Repos, tx *domain.WagerTransaction, events ...domain.Event) error {
	if err := r.Transactions.Update(ctx, tx); err != nil {
		return err
	}
	if err := r.Outbox.Insert(ctx, events...); err != nil {
		return err
	}
	if ext := tx.Snapshot().External; ext != nil && tx.Status().IsTerminal() {
		return r.Transactions.WakePending(ctx, ext.ProviderID, ext.ExternalTransactionID, s.now())
	}
	return nil
}

func (s *Wagers) ResumeNextPending(ctx context.Context) (bool, error) {
	found := false
	err := s.store.InTx(ctx, func(r *store.Repos) error {
		now := s.now()
		view, err := r.Transactions.ClaimDuePending(ctx, now)
		if errors.Is(err, store.ErrNotFound) {
			found = false
			return nil
		}
		if err != nil {
			return err
		}
		found = true

		tx := view.Transaction
		eventCtx := domain.EventContext{CorrelationID: view.CorrelationID, CausationID: tx.ID().String(), OccurredAt: now}
		if eventCtx.CorrelationID == "" {
			eventCtx.CorrelationID = tx.ID().String()
		}

		ref, err := s.findReference(ctx, r, tx)
		if err != nil {
			return err
		}
		if ref != nil && ref.Status().IsTerminal() {
			return s.settle(ctx, r, tx, eventCtx)
		}

		attempts := view.Attempts + 1
		if attempts >= s.pending.MaxAttempts || (view.ExpiresAt != nil && !now.Before(*view.ExpiresAt)) {
			wallet, err := r.Wallets.GetForUpdate(ctx, tx.WalletID())
			if err != nil {
				return err
			}
			return s.reject(ctx, r, tx, domain.ErrReferenceNotFound.Code, wallet, eventCtx)
		}
		return r.Transactions.ReschedulePending(ctx, tx.ID(), attempts, now.Add(s.backoff(attempts)))
	})
	return found, err
}

func (s *Wagers) backoff(attempts int) time.Duration {
	delay := s.pending.BaseBackoff
	for i := 0; i < attempts && delay < s.pending.MaxBackoff; i++ {
		delay *= 2
	}
	delay = min(delay, s.pending.MaxBackoff)
	return delay + time.Duration(rand.Int64N(int64(delay)/10+1))
}

func (s *Wagers) newTransaction(req domain.WagerRequest) (*domain.WagerTransaction, error) {
	return domain.NewExternalTransaction(req.Kind, req.WalletID, req.PlayerID, req.Money, req.ExternalDetails(), s.now())
}

func (s *Wagers) eventContext(correlationID, causationID string) domain.EventContext {
	return domain.EventContext{CorrelationID: correlationID, CausationID: causationID, OccurredAt: s.now()}
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

type TransactionView = store.TransactionView

func (s *Wagers) Get(ctx context.Context, id uuid.UUID) (TransactionView, error) {
	v, err := s.store.Read().Transactions.GetView(ctx, id)
	return v, translate(ctx, err)
}

func (s *Wagers) GetByExternalID(ctx context.Context, providerID, externalID string) (TransactionView, error) {
	v, err := s.store.Read().Transactions.FindViewByExternalID(ctx, providerID, externalID)
	return v, translate(ctx, err)
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
