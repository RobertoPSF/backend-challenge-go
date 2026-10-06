package domain

import (
	"fmt"
	"time"
)

func ApplyToWallet(w *Wallet, t *WagerTransaction, now time.Time) (*LedgerEntry, error) {
	if t.PlayerID() != w.PlayerID() {
		return nil, fmt.Errorf("%w: wallet %s belongs to another player", ErrPlayerWalletMismatch, w.ID())
	}
	if t.Money().Currency() != w.Currency() {
		return nil, fmt.Errorf("%w: wallet is %s, operation is %s", ErrCurrencyMismatch, w.Currency(), t.Money().Currency())
	}

	var entry LedgerEntry
	var err error
	switch t.Kind() {
	case KindBet:
		entry, err = w.Debit(t.ID(), t.Money(), now)
	case KindWin:
		entry, err = w.Credit(t.ID(), t.Money(), now)
	case KindLoss:
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: %s is not applied directly to a wallet", ErrInvalidTransition, t.Kind())
	}
	if err != nil {
		return nil, err
	}
	return &entry, nil
}
