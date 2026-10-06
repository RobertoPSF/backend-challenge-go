package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        Money
	balanceBefore Money
	balanceAfter  Money
	createdAt     time.Time
}

func NewLedgerEntry(walletID, transactionID uuid.UUID, direction Direction, amount, balanceBefore, balanceAfter Money, now time.Time) (LedgerEntry, error) {
	return RehydrateLedgerEntry(newID(), walletID, transactionID, direction, amount, balanceBefore, balanceAfter, now)
}

func RehydrateLedgerEntry(id, walletID, transactionID uuid.UUID, direction Direction, amount, balanceBefore, balanceAfter Money, createdAt time.Time) (LedgerEntry, error) {
	e := LedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     normalizeTime(createdAt),
	}
	if err := e.validate(); err != nil {
		return LedgerEntry{}, err
	}
	return e, nil
}

func (e LedgerEntry) validate() error {
	if e.id == uuid.Nil || e.walletID == uuid.Nil || e.transactionID == uuid.Nil || e.createdAt.IsZero() {
		return fmt.Errorf("%w: missing identity or timestamp", ErrInvalidLedgerEntry)
	}
	if !e.amount.IsPositive() {
		return fmt.Errorf("%w: amount must be positive", ErrInvalidLedgerEntry)
	}
	if e.balanceBefore.IsNegative() || e.balanceAfter.IsNegative() {
		return fmt.Errorf("%w: balances cannot be negative", ErrInvalidLedgerEntry)
	}

	var expected Money
	var err error
	switch e.direction {
	case DirectionCredit:
		expected, err = e.balanceBefore.Add(e.amount)
	case DirectionDebit:
		expected, err = e.balanceBefore.Sub(e.amount)
	default:
		return fmt.Errorf("%w: unknown direction %q", ErrInvalidLedgerEntry, e.direction)
	}
	if err != nil {
		return err
	}
	if !expected.Equal(e.balanceAfter) {
		return fmt.Errorf("%w: balanceAfter %s does not match balanceBefore %s %s %s",
			ErrInvalidLedgerEntry, e.balanceAfter, e.balanceBefore, e.direction, e.amount)
	}
	return nil
}

func (e LedgerEntry) ID() uuid.UUID            { return e.id }
func (e LedgerEntry) WalletID() uuid.UUID      { return e.walletID }
func (e LedgerEntry) TransactionID() uuid.UUID { return e.transactionID }
func (e LedgerEntry) Direction() Direction     { return e.direction }
func (e LedgerEntry) Amount() Money            { return e.amount }
func (e LedgerEntry) BalanceBefore() Money     { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() Money      { return e.balanceAfter }
func (e LedgerEntry) CreatedAt() time.Time     { return e.createdAt }
