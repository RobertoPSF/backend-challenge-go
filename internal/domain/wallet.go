package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const InitialWalletVersion int64 = 1

type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func RehydrateWallet(id, playerID uuid.UUID, balance Money, version int64, createdAt, updatedAt time.Time) (*Wallet, error) {
	w := &Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}
	if err := w.validate(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Wallet) Debit(transactionID uuid.UUID, amount Money, now time.Time) (LedgerEntry, error) {
	return w.applyChange(DirectionDebit, transactionID, amount, now, true)
}

func (w *Wallet) Credit(transactionID uuid.UUID, amount Money, now time.Time) (LedgerEntry, error) {
	return w.applyChange(DirectionCredit, transactionID, amount, now, true)
}

func (w *Wallet) applyChange(direction Direction, transactionID uuid.UUID, amount Money, now time.Time, bumpVersion bool) (LedgerEntry, error) {
	if err := w.validate(); err != nil {
		return LedgerEntry{}, err
	}
	if !amount.IsValid() {
		return LedgerEntry{}, ErrInvalidMoney
	}
	if amount.Currency() != w.Currency() {
		return LedgerEntry{}, fmt.Errorf("%w: wallet is %s, movement is %s", ErrCurrencyMismatch, w.Currency(), amount.Currency())
	}
	if !amount.IsPositive() {
		return LedgerEntry{}, fmt.Errorf("%w: movement must be greater than zero", ErrInvalidAmount)
	}

	var after Money
	var err error
	if direction == DirectionCredit {
		after, err = w.balance.Add(amount)
	} else {
		after, err = w.balance.Sub(amount)
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	if after.IsNegative() {
		return LedgerEntry{}, fmt.Errorf("%w: balance %s, debit %s", ErrInsufficientFunds, w.balance, amount)
	}

	entry, err := NewLedgerEntry(w.id, transactionID, direction, amount, w.balance, after, now)
	if err != nil {
		return LedgerEntry{}, err
	}

	w.balance = after
	w.updatedAt = entry.CreatedAt()
	if bumpVersion {
		w.version++
	}
	return entry, nil
}

func (w *Wallet) validate() error {
	if w == nil || w.id == uuid.Nil || w.playerID == uuid.Nil {
		return fmt.Errorf("%w: missing identity", ErrInvalidWallet)
	}
	if !w.balance.IsValid() || w.balance.IsNegative() {
		return fmt.Errorf("%w: balance must be a valid non-negative amount", ErrInvalidWallet)
	}
	if w.version < InitialWalletVersion {
		return fmt.Errorf("%w: version must be >= %d", ErrInvalidWallet, InitialWalletVersion)
	}
	if w.createdAt.IsZero() || w.updatedAt.IsZero() {
		return fmt.Errorf("%w: missing timestamps", ErrInvalidWallet)
	}
	return nil
}

func (w *Wallet) ID() uuid.UUID        { return w.id }
func (w *Wallet) PlayerID() uuid.UUID  { return w.playerID }
func (w *Wallet) Balance() Money       { return w.balance }
func (w *Wallet) Currency() Currency   { return w.balance.Currency() }
func (w *Wallet) Version() int64       { return w.version }
func (w *Wallet) CreatedAt() time.Time { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }
