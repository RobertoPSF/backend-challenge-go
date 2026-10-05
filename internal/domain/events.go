package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	EventWagerTransactionProcessed        = "WagerTransactionProcessed"
	EventWagerTransactionRejected         = "WagerTransactionRejected"
	EventWalletBalanceChanged             = "WalletBalanceChanged"
	EventWagerTransactionPendingReference = "WagerTransactionPendingReference"

	eventVersion = 1
)

type Event interface {
	Header() EventHeader
}

type EventHeader struct {
	EventID       uuid.UUID `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   uuid.UUID `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   *string   `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
}

func (h EventHeader) Header() EventHeader { return h }

type EventContext struct {
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

func newEventHeader(eventType string, aggregateID uuid.UUID, ctx EventContext) (EventHeader, error) {
	if ctx.CorrelationID == "" {
		return EventHeader{}, fmt.Errorf("%w: %s requires a correlationId", ErrInvalidEvent, eventType)
	}
	if ctx.OccurredAt.IsZero() {
		return EventHeader{}, fmt.Errorf("%w: %s requires occurredAt", ErrInvalidEvent, eventType)
	}
	h := EventHeader{
		EventID:       newID(),
		EventType:     eventType,
		AggregateID:   aggregateID,
		CorrelationID: ctx.CorrelationID,
		OccurredAt:    normalizeTime(ctx.OccurredAt),
		Version:       eventVersion,
	}
	if ctx.CausationID != "" {
		causation := ctx.CausationID
		h.CausationID = &causation
	}
	return h, nil
}

type TransactionRef struct {
	TransactionID                  uuid.UUID  `json:"transactionId"`
	WalletID                       uuid.UUID  `json:"walletId"`
	PlayerID                       uuid.UUID  `json:"playerId"`
	Kind                           Kind       `json:"kind"`
	Money                          Money      `json:"money"`
	ProviderID                     string     `json:"providerId,omitempty"`
	ExternalTransactionID          string     `json:"externalTransactionId,omitempty"`
	RoundID                        string     `json:"roundId,omitempty"`
	GameID                         string     `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         *uuid.UUID `json:"referenceTransactionId,omitempty"`
}

func newTransactionRef(s WagerTransactionSnapshot) TransactionRef {
	ref := TransactionRef{
		TransactionID:          s.ID,
		WalletID:               s.WalletID,
		PlayerID:               s.PlayerID,
		Kind:                   s.Kind,
		Money:                  s.Money,
		ReferenceTransactionID: s.ReferenceTransactionID,
	}
	if s.External != nil {
		ref.ProviderID = s.External.ProviderID
		ref.ExternalTransactionID = s.External.ExternalTransactionID
		ref.RoundID = s.External.RoundID
		ref.GameID = s.External.GameID
		ref.ReferenceExternalTransactionID = s.External.ReferenceExternalTransactionID
	}
	return ref
}

type WagerTransactionProcessedData struct {
	TransactionRef
	BalanceAfter Money     `json:"balanceAfter"`
	ProcessedAt  time.Time `json:"processedAt"`
}

type WagerTransactionProcessed struct {
	EventHeader
	Data WagerTransactionProcessedData `json:"data"`
}

func NewWagerTransactionProcessed(t *WagerTransaction, ctx EventContext) (WagerTransactionProcessed, error) {
	s := t.Snapshot()
	if s.Status != StatusProcessed {
		return WagerTransactionProcessed{}, fmt.Errorf("%w: transaction is %s, not PROCESSED", ErrInvalidTransition, s.Status)
	}
	h, err := newEventHeader(EventWagerTransactionProcessed, s.WalletID, ctx)
	if err != nil {
		return WagerTransactionProcessed{}, err
	}
	return WagerTransactionProcessed{EventHeader: h, Data: WagerTransactionProcessedData{
		TransactionRef: newTransactionRef(s),
		BalanceAfter:   *s.BalanceAfter,
		ProcessedAt:    *s.ProcessedAt,
	}}, nil
}

type WagerTransactionRejectedData struct {
	TransactionRef
	FailureCode     FailureCode `json:"failureCode"`
	ObservedBalance Money       `json:"observedBalance"`
	RejectedAt      time.Time   `json:"rejectedAt"`
}

type WagerTransactionRejected struct {
	EventHeader
	Data WagerTransactionRejectedData `json:"data"`
}

func NewWagerTransactionRejected(t *WagerTransaction, ctx EventContext) (WagerTransactionRejected, error) {
	s := t.Snapshot()
	if s.Status != StatusRejected {
		return WagerTransactionRejected{}, fmt.Errorf("%w: transaction is %s, not REJECTED", ErrInvalidTransition, s.Status)
	}
	h, err := newEventHeader(EventWagerTransactionRejected, s.WalletID, ctx)
	if err != nil {
		return WagerTransactionRejected{}, err
	}
	return WagerTransactionRejected{EventHeader: h, Data: WagerTransactionRejectedData{
		TransactionRef:  newTransactionRef(s),
		FailureCode:     s.FailureCode,
		ObservedBalance: *s.BalanceAfter,
		RejectedAt:      *s.ProcessedAt,
	}}, nil
}

type WagerTransactionPendingReferenceData struct {
	TransactionRef
}

type WagerTransactionPendingReference struct {
	EventHeader
	Data WagerTransactionPendingReferenceData `json:"data"`
}

func NewWagerTransactionPendingReference(t *WagerTransaction, ctx EventContext) (WagerTransactionPendingReference, error) {
	s := t.Snapshot()
	if s.Status != StatusPendingReference {
		return WagerTransactionPendingReference{}, fmt.Errorf("%w: transaction is %s, not PENDING_REFERENCE", ErrInvalidTransition, s.Status)
	}
	h, err := newEventHeader(EventWagerTransactionPendingReference, s.WalletID, ctx)
	if err != nil {
		return WagerTransactionPendingReference{}, err
	}
	return WagerTransactionPendingReference{EventHeader: h, Data: WagerTransactionPendingReferenceData{
		TransactionRef: newTransactionRef(s),
	}}, nil
}

type WalletBalanceChangedData struct {
	WalletID      uuid.UUID `json:"walletId"`
	TransactionID uuid.UUID `json:"transactionId"`
	Direction     Direction `json:"direction"`
	Money         Money     `json:"money"`
	BalanceBefore Money     `json:"balanceBefore"`
	BalanceAfter  Money     `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
}

type WalletBalanceChanged struct {
	EventHeader
	Data WalletBalanceChangedData `json:"data"`
}

func NewWalletBalanceChanged(entry LedgerEntry, walletVersion int64, ctx EventContext) (WalletBalanceChanged, error) {
	if entry.ID() == uuid.Nil {
		return WalletBalanceChanged{}, fmt.Errorf("%w: missing ledger entry", ErrInvalidLedgerEntry)
	}
	h, err := newEventHeader(EventWalletBalanceChanged, entry.WalletID(), ctx)
	if err != nil {
		return WalletBalanceChanged{}, err
	}
	return WalletBalanceChanged{EventHeader: h, Data: WalletBalanceChangedData{
		WalletID:      entry.WalletID(),
		TransactionID: entry.TransactionID(),
		Direction:     entry.Direction(),
		Money:         entry.Amount(),
		BalanceBefore: entry.BalanceBefore(),
		BalanceAfter:  entry.BalanceAfter(),
		WalletVersion: walletVersion,
	}}, nil
}
