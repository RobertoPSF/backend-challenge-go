package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

func ParseExternalKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return k, nil
	case KindOpening:
		return "", fmt.Errorf("%w: %s is reserved for internal wallet opening", ErrUnsupportedKind, k)
	default:
		return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidTransaction, s)
	}
}

func (k Kind) IsReversal() bool { return k == KindRefund || k == KindRollback }

type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

type ExternalDetails struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
}

type WagerTransactionSnapshot struct {
	ID                     uuid.UUID
	Kind                   Kind
	Status                 Status
	WalletID               uuid.UUID
	PlayerID               uuid.UUID
	Money                  Money
	External               *ExternalDetails
	ReferenceTransactionID *uuid.UUID
	FailureCode            FailureCode
	BalanceAfter           *Money
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ProcessedAt            *time.Time
}

type WagerTransaction struct {
	s WagerTransactionSnapshot
}

func NewExternalTransaction(kind Kind, walletID, playerID uuid.UUID, money Money, ext ExternalDetails, now time.Time) (*WagerTransaction, error) {
	now = normalizeTime(now)
	t := &WagerTransaction{s: WagerTransactionSnapshot{
		ID:        newID(),
		Kind:      kind,
		Status:    StatusPending,
		WalletID:  walletID,
		PlayerID:  playerID,
		Money:     money,
		External:  &ext,
		CreatedAt: now,
		UpdatedAt: now,
	}}
	if kind == KindOpening {
		return nil, fmt.Errorf("%w: %s is reserved for internal wallet opening", ErrUnsupportedKind, kind)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	return t, nil
}

func newOpeningTransaction(walletID, playerID uuid.UUID, amount Money, now time.Time) (*WagerTransaction, error) {
	now = normalizeTime(now)
	t := &WagerTransaction{s: WagerTransactionSnapshot{
		ID:        newID(),
		Kind:      KindOpening,
		Status:    StatusPending,
		WalletID:  walletID,
		PlayerID:  playerID,
		Money:     amount,
		CreatedAt: now,
		UpdatedAt: now,
	}}
	if err := t.validate(); err != nil {
		return nil, err
	}
	return t, nil
}

func RehydrateWagerTransaction(s WagerTransactionSnapshot) (*WagerTransaction, error) {
	s = s.clone()
	s.CreatedAt = normalizeTime(s.CreatedAt)
	s.UpdatedAt = normalizeTime(s.UpdatedAt)
	if s.ProcessedAt != nil {
		*s.ProcessedAt = normalizeTime(*s.ProcessedAt)
	}
	t := &WagerTransaction{s: s}
	if err := t.validate(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *WagerTransaction) MarkProcessed(balanceAfter Money, referenceTransactionID *uuid.UUID, now time.Time) error {
	if err := t.transition(StatusProcessed, StatusPending, StatusPendingReference); err != nil {
		return err
	}
	if !balanceAfter.IsValid() || balanceAfter.IsNegative() || balanceAfter.Currency() != t.s.Money.Currency() {
		return fmt.Errorf("%w: invalid balance after processing", ErrInvalidTransition)
	}
	if t.HasReference() && referenceTransactionID == nil {
		return fmt.Errorf("%w: %s requires the resolved reference", ErrInvalidTransition, t.s.Kind)
	}
	t.s.BalanceAfter = &balanceAfter
	t.s.ReferenceTransactionID = referenceTransactionID
	t.finish(StatusProcessed, now)
	return nil
}

func (t *WagerTransaction) MarkRejected(code FailureCode, observedBalance Money, now time.Time) error {
	if err := t.transition(StatusRejected, StatusPending, StatusPendingReference); err != nil {
		return err
	}
	if !observedBalance.IsValid() || observedBalance.IsNegative() {
		return fmt.Errorf("%w: invalid balance observed on rejection", ErrInvalidTransition)
	}
	if err := t.finishWithCode(StatusRejected, code, now); err != nil {
		return err
	}
	t.s.BalanceAfter = &observedBalance
	return nil
}

func (t *WagerTransaction) MarkFailed(code FailureCode, now time.Time) error {
	if err := t.transition(StatusFailed, StatusPending, StatusPendingReference); err != nil {
		return err
	}
	return t.finishWithCode(StatusFailed, code, now)
}

func (t *WagerTransaction) MarkPendingReference(now time.Time) error {
	if err := t.transition(StatusPendingReference, StatusPending); err != nil {
		return err
	}
	if !t.HasReference() {
		return fmt.Errorf("%w: only operations with a reference can wait for it", ErrInvalidTransition)
	}
	t.s.Status = StatusPendingReference
	t.s.UpdatedAt = normalizeTime(now)
	return nil
}

func (t *WagerTransaction) transition(to Status, allowedFrom ...Status) error {
	if t.s.Status.IsTerminal() {
		return fmt.Errorf("%w: %s cannot move to %s", ErrTerminalState, t.s.Status, to)
	}
	for _, from := range allowedFrom {
		if t.s.Status == from {
			return nil
		}
	}
	return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.s.Status, to)
}

func (t *WagerTransaction) finishWithCode(status Status, code FailureCode, now time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: %s requires a failure code", ErrInvalidTransition, status)
	}
	t.s.FailureCode = code
	t.finish(status, now)
	return nil
}

func (t *WagerTransaction) finish(status Status, now time.Time) {
	now = normalizeTime(now)
	t.s.Status = status
	t.s.UpdatedAt = now
	t.s.ProcessedAt = &now
}

func (t *WagerTransaction) validate() error {
	s := t.s
	if s.ID == uuid.Nil || s.WalletID == uuid.Nil || s.PlayerID == uuid.Nil {
		return fmt.Errorf("%w: missing identity", ErrInvalidTransaction)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: missing timestamps", ErrInvalidTransaction)
	}
	switch s.Status {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
	default:
		return fmt.Errorf("%w: unknown status %q", ErrInvalidTransaction, s.Status)
	}
	if !s.Money.IsValid() {
		return ErrInvalidMoney
	}
	if s.Money.IsNegative() {
		return fmt.Errorf("%w: amount cannot be negative", ErrInvalidAmount)
	}
	if err := validateAmountForKind(s.Kind, s.Money); err != nil {
		return err
	}
	if err := validateOrigin(s.Kind, s.External); err != nil {
		return err
	}
	if s.Status.IsTerminal() && s.ProcessedAt == nil {
		return fmt.Errorf("%w: terminal transaction without processedAt", ErrInvalidTransaction)
	}
	if (s.Status == StatusProcessed || s.Status == StatusRejected) && s.BalanceAfter == nil {
		return fmt.Errorf("%w: %s transaction without balanceAfter", ErrInvalidTransaction, s.Status)
	}
	if (s.Status == StatusRejected || s.Status == StatusFailed) && s.FailureCode == "" {
		return fmt.Errorf("%w: %s transaction without failure code", ErrInvalidTransaction, s.Status)
	}
	return nil
}

func validateAmountForKind(kind Kind, money Money) error {
	switch kind {
	case KindLoss:
		if !money.IsZero() {
			return fmt.Errorf("%w: LOSS amount must be 0.00", ErrInvalidAmount)
		}
	case KindOpening, KindBet, KindWin, KindRefund, KindRollback:
		if !money.IsPositive() {
			return fmt.Errorf("%w: %s amount must be greater than zero", ErrInvalidAmount, kind)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidTransaction, kind)
	}
	return nil
}

func validateOrigin(kind Kind, ext *ExternalDetails) error {
	if kind == KindOpening {
		if ext != nil {
			return fmt.Errorf("%w: OPENING cannot carry external metadata", ErrInvalidTransaction)
		}
		return nil
	}
	if ext == nil {
		return fmt.Errorf("%w: %s requires external metadata", ErrInvalidTransaction, kind)
	}
	required := map[string]string{
		"providerId":            ext.ProviderID,
		"externalTransactionId": ext.ExternalTransactionID,
		"idempotencyKey":        ext.IdempotencyKey,
		"payloadHash":           ext.PayloadHash,
		"roundId":               ext.RoundID,
		"gameId":                ext.GameID,
	}
	for field, value := range required {
		if value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidTransaction, field)
		}
	}
	hasReference := ext.ReferenceExternalTransactionID != ""
	if kind.IsReversal() && !hasReference {
		return fmt.Errorf("%w: %s requires referenceExternalTransactionId", ErrInvalidTransaction, kind)
	}
	if (kind == KindBet || kind == KindLoss) && hasReference {
		return fmt.Errorf("%w: %s does not accept referenceExternalTransactionId", ErrInvalidTransaction, kind)
	}
	return nil
}

func (t *WagerTransaction) Snapshot() WagerTransactionSnapshot {
	return t.s.clone()
}

func (s WagerTransactionSnapshot) clone() WagerTransactionSnapshot {
	s.External = clonePtr(s.External)
	s.ReferenceTransactionID = clonePtr(s.ReferenceTransactionID)
	s.BalanceAfter = clonePtr(s.BalanceAfter)
	s.ProcessedAt = clonePtr(s.ProcessedAt)
	return s
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func (t *WagerTransaction) ID() uuid.UUID       { return t.s.ID }
func (t *WagerTransaction) Kind() Kind          { return t.s.Kind }
func (t *WagerTransaction) Status() Status      { return t.s.Status }
func (t *WagerTransaction) WalletID() uuid.UUID { return t.s.WalletID }
func (t *WagerTransaction) PlayerID() uuid.UUID { return t.s.PlayerID }
func (t *WagerTransaction) Money() Money        { return t.s.Money }
func (t *WagerTransaction) IsExternal() bool    { return t.s.External != nil }

func (t *WagerTransaction) HasReference() bool {
	return t.s.External != nil && t.s.External.ReferenceExternalTransactionID != ""
}
