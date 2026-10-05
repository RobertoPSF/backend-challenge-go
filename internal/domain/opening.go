package domain

import (
	"fmt"

	"github.com/google/uuid"
)

type WalletOpening struct {
	Wallet      *Wallet
	Transaction *WagerTransaction
	LedgerEntry *LedgerEntry
	Events      []Event
}

func OpenWallet(playerID uuid.UUID, initialBalance Money, ctx EventContext) (WalletOpening, error) {
	if playerID == uuid.Nil {
		return WalletOpening{}, fmt.Errorf("%w: playerId is required", ErrInvalidTransaction)
	}
	if !initialBalance.IsValid() {
		return WalletOpening{}, ErrInvalidMoney
	}
	if initialBalance.IsNegative() {
		return WalletOpening{}, fmt.Errorf("%w: initial balance cannot be negative", ErrInvalidAmount)
	}

	now := normalizeTime(ctx.OccurredAt)
	wallet, err := RehydrateWallet(newID(), playerID, Zero(initialBalance.Currency()), InitialWalletVersion, now, now)
	if err != nil {
		return WalletOpening{}, err
	}
	if initialBalance.IsZero() {
		return WalletOpening{Wallet: wallet}, nil
	}

	tx, err := newOpeningTransaction(wallet.ID(), playerID, initialBalance, now)
	if err != nil {
		return WalletOpening{}, err
	}
	entry, err := wallet.applyChange(DirectionCredit, tx.ID(), initialBalance, now, false)
	if err != nil {
		return WalletOpening{}, err
	}
	if err := tx.MarkProcessed(wallet.Balance(), nil, now); err != nil {
		return WalletOpening{}, err
	}

	processed, err := NewWagerTransactionProcessed(tx, ctx)
	if err != nil {
		return WalletOpening{}, err
	}
	balanceChanged, err := NewWalletBalanceChanged(entry, wallet.Version(), ctx)
	if err != nil {
		return WalletOpening{}, err
	}

	return WalletOpening{
		Wallet:      wallet,
		Transaction: tx,
		LedgerEntry: &entry,
		Events:      []Event{processed, balanceChanged},
	}, nil
}
