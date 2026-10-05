package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var eventCtx = EventContext{CorrelationID: "corr-1", OccurredAt: now}

func TestOpenWallet_WithPositiveBalance(t *testing.T) {
	playerID := uuid.New()
	op, err := OpenWallet(playerID, mustMoney(t, "1000.00"), eventCtx)
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}

	w := op.Wallet
	if w.PlayerID() != playerID || w.Balance().String() != "1000.00" || w.Currency() != BRL || w.Version() != 1 {
		t.Errorf("wallet = player %s balance %s version %d", w.PlayerID(), w.Balance(), w.Version())
	}

	tx := op.Transaction.Snapshot()
	if tx.Kind != KindOpening || tx.Status != StatusProcessed || tx.External != nil {
		t.Errorf("opening tx = kind %s status %s external %+v", tx.Kind, tx.Status, tx.External)
	}
	if tx.WalletID != w.ID() || tx.PlayerID != playerID || tx.Money.String() != "1000.00" || tx.BalanceAfter.String() != "1000.00" {
		t.Errorf("opening tx data = %+v", tx)
	}

	e := op.LedgerEntry
	if e.Direction() != DirectionCredit || e.BalanceBefore().String() != "0.00" || e.BalanceAfter().String() != "1000.00" || e.TransactionID() != tx.ID {
		t.Errorf("opening entry = %+v", e)
	}

	if len(op.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(op.Events))
	}
	processed, ok := op.Events[0].(WagerTransactionProcessed)
	if !ok || processed.Data.Kind != KindOpening || processed.Data.ProviderID != "" || processed.AggregateID != w.ID() {
		t.Errorf("first event = %+v", op.Events[0])
	}
	changed, ok := op.Events[1].(WalletBalanceChanged)
	if !ok || changed.Data.WalletVersion != 1 || changed.Data.BalanceAfter.String() != "1000.00" || changed.Data.Direction != DirectionCredit {
		t.Errorf("second event = %+v", op.Events[1])
	}
}

func TestOpenWallet_WithZeroBalanceHasNoFinancialRecords(t *testing.T) {
	op, err := OpenWallet(uuid.New(), Zero(BRL), eventCtx)
	if err != nil {
		t.Fatal(err)
	}
	if op.Transaction != nil || op.LedgerEntry != nil || len(op.Events) != 0 {
		t.Errorf("zero opening must not create OPENING, ledger or events: %+v", op)
	}
	if !op.Wallet.Balance().IsZero() || op.Wallet.Version() != 1 {
		t.Errorf("wallet = %s v%d", op.Wallet.Balance(), op.Wallet.Version())
	}
}

func TestOpenWallet_RejectsInvalidInput(t *testing.T) {
	negative := mustNeg(t, mustMoney(t, "1.00"))
	tests := map[string]struct {
		player  uuid.UUID
		balance Money
		ctx     EventContext
		want    error
	}{
		"nil player":       {uuid.Nil, mustMoney(t, "1.00"), eventCtx, ErrInvalidTransaction},
		"invalid money":    {uuid.New(), Money{}, eventCtx, ErrInvalidMoney},
		"negative balance": {uuid.New(), negative, eventCtx, ErrInvalidAmount},
		"no correlation":   {uuid.New(), mustMoney(t, "1.00"), EventContext{OccurredAt: now}, ErrInvalidEvent},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := OpenWallet(tt.player, tt.balance, tt.ctx); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestEvents_EnvelopeJSON(t *testing.T) {
	op, err := OpenWallet(uuid.New(), mustMoney(t, "25.00"), EventContext{
		CorrelationID: "corr-1",
		CausationID:   "msg-123",
		OccurredAt:    time.Date(2026, 9, 8, 9, 0, 0, 123456789, time.FixedZone("BRT", -3*3600)),
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(op.Events[1])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)

	for _, field := range []string{"eventId", "eventType", "aggregateId", "correlationId", "causationId", "occurredAt", "version", "data"} {
		if _, ok := got[field]; !ok {
			t.Errorf("envelope missing %q: %s", field, raw)
		}
	}
	if got["eventType"] != EventWalletBalanceChanged || got["version"] != float64(1) || got["causationId"] != "msg-123" {
		t.Errorf("header = %s", raw)
	}
	if got["occurredAt"] != "2026-09-08T12:00:00.123456Z" {
		t.Errorf("occurredAt = %v, want UTC RFC 3339", got["occurredAt"])
	}

	data := got["data"].(map[string]any)
	for _, field := range []string{"walletId", "transactionId", "direction", "money", "balanceBefore", "balanceAfter", "walletVersion"} {
		if _, ok := data[field]; !ok {
			t.Errorf("WalletBalanceChanged data missing %q", field)
		}
	}
	money := data["money"].(map[string]any)
	if money["amount"] != "25.00" || money["currency"] != "BRL" {
		t.Errorf("money = %v, want decimal string", money)
	}
}

func TestEvents_RequireMatchingStatus(t *testing.T) {
	tx := newTx(t, KindRefund, "10.00")

	if _, err := NewWagerTransactionProcessed(tx, eventCtx); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Processed for PENDING error = %v", err)
	}
	if _, err := NewWagerTransactionRejected(tx, eventCtx); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Rejected for PENDING error = %v", err)
	}
	if _, err := NewWagerTransactionPendingReference(tx, eventCtx); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("PendingReference for PENDING error = %v", err)
	}

	_ = tx.MarkPendingReference(now)
	pending, err := NewWagerTransactionPendingReference(tx, eventCtx)
	if err != nil || pending.EventType != EventWagerTransactionPendingReference || pending.Data.ReferenceExternalTransactionID != "transaction-000" {
		t.Errorf("PendingReference = %+v, %v", pending, err)
	}

	_ = tx.MarkRejected("REFERENCE_NOT_FOUND", mustMoney(t, "50.00"), now)
	rejected, err := NewWagerTransactionRejected(tx, eventCtx)
	if err != nil || rejected.Data.FailureCode != "REFERENCE_NOT_FOUND" || rejected.Data.ProviderID != "provider-a" ||
		rejected.Data.ObservedBalance.String() != "50.00" {
		t.Errorf("Rejected = %+v, %v", rejected, err)
	}
	if rejected.EventID == pending.EventID {
		t.Error("each event must have its own eventId")
	}
}
