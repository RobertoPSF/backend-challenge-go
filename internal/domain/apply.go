package domain

import (
	"errors"
	"fmt"
	"time"
)

func ApplyToWallet(w *Wallet, t *WagerTransaction, ref *WagerTransaction, alreadyReversed bool, now time.Time) (*LedgerEntry, error) {
	if t.PlayerID() != w.PlayerID() {
		return nil, fmt.Errorf("%w: wallet %s belongs to another player", ErrPlayerWalletMismatch, w.ID())
	}
	if t.Money().Currency() != w.Currency() {
		return nil, fmt.Errorf("%w: wallet is %s, operation is %s", ErrCurrencyMismatch, w.Currency(), t.Money().Currency())
	}
	if t.HasReference() {
		if err := checkReference(t, ref); err != nil {
			return nil, err
		}
	}

	switch t.Kind() {
	case KindBet:
		return movement(w.Debit(t.ID(), t.Money(), now))
	case KindLoss:
		return nil, nil
	case KindWin:
		if ref != nil && ref.Kind() != KindBet {
			return nil, fmt.Errorf("%w: WIN must reference a BET, got %s", ErrReferenceMismatch, ref.Kind())
		}
		return movement(w.Credit(t.ID(), t.Money(), now))
	case KindRefund, KindRollback:
		return applyReversal(w, t, ref, alreadyReversed, now)
	default:
		return nil, fmt.Errorf("%w: %s is not applied to a wallet", ErrInvalidTransition, t.Kind())
	}
}

func checkReference(t, ref *WagerTransaction) error {
	if ref == nil {
		return fmt.Errorf("%w: %s requires its reference", ErrInvalidTransition, t.Kind())
	}
	if ref.Status() != StatusProcessed {
		return fmt.Errorf("%w: reference is %s", ErrReferenceNotProcessed, ref.Status())
	}
	op, rf := t.Snapshot(), ref.Snapshot()
	if rf.External == nil || rf.External.ProviderID != op.External.ProviderID || rf.External.RoundID != op.External.RoundID ||
		rf.PlayerID != op.PlayerID || rf.WalletID != op.WalletID || rf.Money.Currency() != op.Money.Currency() {
		return fmt.Errorf("%w: provider, player, wallet, currency and round must match the reference", ErrReferenceMismatch)
	}
	return nil
}

func applyReversal(w *Wallet, t, ref *WagerTransaction, alreadyReversed bool, now time.Time) (*LedgerEntry, error) {
	var direction Direction
	switch {
	case ref.Kind() == KindBet:
		direction = DirectionCredit
	case t.Kind() == KindRollback && (ref.Kind() == KindWin || ref.Kind() == KindRefund):
		direction = DirectionDebit
	default:
		return nil, fmt.Errorf("%w: %s cannot reverse a %s", ErrReferenceKindNotReversible, t.Kind(), ref.Kind())
	}
	if !t.Money().Equal(ref.Money()) {
		return nil, fmt.Errorf("%w: %s, reference is %s", ErrAmountMismatch, t.Money(), ref.Money())
	}
	if alreadyReversed {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyReversed, ref.ID())
	}

	if direction == DirectionCredit {
		return movement(w.Credit(t.ID(), t.Money(), now))
	}
	entry, err := w.Debit(t.ID(), t.Money(), now)
	if errors.Is(err, ErrInsufficientFunds) {
		return nil, fmt.Errorf("%w: balance %s, reversal %s", ErrReversalInsufficientFunds, w.Balance(), t.Money())
	}
	return movement(entry, err)
}

func movement(entry LedgerEntry, err error) (*LedgerEntry, error) {
	if err != nil {
		return nil, err
	}
	return &entry, nil
}
