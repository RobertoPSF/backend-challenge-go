package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validExternal(ref string) ExternalDetails {
	return ExternalDetails{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "transaction-123",
		IdempotencyKey:                 "provider-a:transaction-123",
		PayloadHash:                    "hash",
		RoundID:                        "round-987",
		GameID:                         "fortune-chimp",
		ReferenceExternalTransactionID: ref,
	}
}

func newTx(t *testing.T, kind Kind, amount string) *WagerTransaction {
	t.Helper()
	ref := ""
	if kind.IsReversal() {
		ref = "transaction-000"
	}
	tx, err := NewExternalTransaction(kind, uuid.New(), uuid.New(), mustMoney(t, amount), validExternal(ref), now)
	if err != nil {
		t.Fatalf("NewExternalTransaction(%s): %v", kind, err)
	}
	return tx
}

func TestParseExternalKind(t *testing.T) {
	for _, k := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		if got, err := ParseExternalKind(k); err != nil || string(got) != k {
			t.Errorf("ParseExternalKind(%s) = %s, %v", k, got, err)
		}
	}
	if _, err := ParseExternalKind("OPENING"); !errors.Is(err, ErrUnsupportedKind) {
		t.Errorf("OPENING error = %v, want ErrUnsupportedKind", err)
	}
	for _, k := range []string{"", "bet", "CASHOUT"} {
		if _, err := ParseExternalKind(k); !errors.Is(err, ErrInvalidTransaction) {
			t.Errorf("ParseExternalKind(%q) error = %v, want ErrInvalidTransaction", k, err)
		}
	}
}

func TestNewExternalTransaction_ZeroAmountPolicy(t *testing.T) {
	tests := []struct {
		kind    Kind
		amount  string
		wantErr error
	}{
		{KindLoss, "0.00", nil},
		{KindLoss, "0.01", ErrInvalidAmount},
		{KindBet, "0.01", nil},
		{KindBet, "0.00", ErrInvalidAmount},
		{KindWin, "0.00", ErrInvalidAmount},
		{KindRefund, "0.00", ErrInvalidAmount},
		{KindRollback, "0.00", ErrInvalidAmount},
		{KindWin, "10.00", nil},
		{KindRefund, "10.00", nil},
		{KindRollback, "10.00", nil},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind)+"_"+tt.amount, func(t *testing.T) {
			ref := ""
			if tt.kind.IsReversal() {
				ref = "transaction-000"
			}
			tx, err := NewExternalTransaction(tt.kind, uuid.New(), uuid.New(), mustMoney(t, tt.amount), validExternal(ref), now)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if err == nil && (tx.Status() != StatusPending || !tx.IsExternal()) {
				t.Errorf("new transaction must be an external PENDING, got %s", tx.Status())
			}
		})
	}
}

func TestNewExternalTransaction_RejectsOpening(t *testing.T) {
	_, err := NewExternalTransaction(KindOpening, uuid.New(), uuid.New(), mustMoney(t, "10.00"), validExternal(""), now)
	if !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("error = %v, want ErrUnsupportedKind", err)
	}
}

func TestNewExternalTransaction_ValidatesMetadata(t *testing.T) {
	tests := map[string]struct {
		kind   Kind
		mutate func(*ExternalDetails)
	}{
		"missing provider":         {KindBet, func(e *ExternalDetails) { e.ProviderID = "" }},
		"missing external id":      {KindBet, func(e *ExternalDetails) { e.ExternalTransactionID = "" }},
		"missing idempotency key":  {KindBet, func(e *ExternalDetails) { e.IdempotencyKey = "" }},
		"missing payload hash":     {KindBet, func(e *ExternalDetails) { e.PayloadHash = "" }},
		"missing round":            {KindBet, func(e *ExternalDetails) { e.RoundID = "" }},
		"missing game":             {KindBet, func(e *ExternalDetails) { e.GameID = "" }},
		"refund without reference": {KindRefund, func(e *ExternalDetails) { e.ReferenceExternalTransactionID = "" }},
		"rollback without ref":     {KindRollback, func(e *ExternalDetails) { e.ReferenceExternalTransactionID = "" }},
		"bet with reference":       {KindBet, func(e *ExternalDetails) { e.ReferenceExternalTransactionID = "x" }},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ext := validExternal("transaction-000")
			if !tt.kind.IsReversal() {
				ext.ReferenceExternalTransactionID = ""
			}
			tt.mutate(&ext)
			_, err := NewExternalTransaction(tt.kind, uuid.New(), uuid.New(), mustMoney(t, "1.00"), ext, now)
			if !errors.Is(err, ErrInvalidTransaction) {
				t.Fatalf("error = %v, want ErrInvalidTransaction", err)
			}
		})
	}

	if _, err := NewExternalTransaction(KindWin, uuid.New(), uuid.New(), mustMoney(t, "1.00"), validExternal("bet-1"), now); err != nil {
		t.Errorf("WIN may reference a bet: %v", err)
	}
	if _, err := NewExternalTransaction(KindLoss, uuid.New(), uuid.New(), mustMoney(t, "0.00"), validExternal("bet-1"), now); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("LOSS with reference error = %v, want ErrInvalidTransaction", err)
	}
	if _, err := NewExternalTransaction(KindBet, uuid.Nil, uuid.New(), mustMoney(t, "1.00"), validExternal(""), now); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("missing wallet error = %v, want ErrInvalidTransaction", err)
	}
	if _, err := NewExternalTransaction(KindBet, uuid.New(), uuid.New(), Money{}, validExternal(""), now); !errors.Is(err, ErrInvalidMoney) {
		t.Errorf("invalid money error = %v, want ErrInvalidMoney", err)
	}
}

func TestTransaction_StateMachine(t *testing.T) {
	balance := mustMoney(t, "975.00")
	refID := uuid.New()

	type step func(tx *WagerTransaction) error
	process := func(tx *WagerTransaction) error {
		var ref *uuid.UUID
		if tx.Kind().IsReversal() {
			ref = &refID
		}
		return tx.MarkProcessed(balance, ref, now)
	}
	reject := func(tx *WagerTransaction) error { return tx.MarkRejected("INSUFFICIENT_FUNDS", balance, now) }
	fail := func(tx *WagerTransaction) error { return tx.MarkFailed("INFRASTRUCTURE_PERMANENT_FAILURE", now) }
	pending := func(tx *WagerTransaction) error { return tx.MarkPendingReference(now) }

	tests := []struct {
		name  string
		kind  Kind
		steps []step
		want  Status
	}{
		{"pending -> processed", KindBet, []step{process}, StatusProcessed},
		{"pending -> rejected", KindBet, []step{reject}, StatusRejected},
		{"pending -> failed", KindBet, []step{fail}, StatusFailed},
		{"pending -> pending_reference", KindRefund, []step{pending}, StatusPendingReference},
		{"pending_reference -> processed", KindRefund, []step{pending, process}, StatusProcessed},
		{"pending_reference -> rejected", KindRollback, []step{pending, reject}, StatusRejected},
		{"pending_reference -> failed", KindRollback, []step{pending, fail}, StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := newTx(t, tt.kind, "25.00")
			for _, s := range tt.steps {
				if err := s(tx); err != nil {
					t.Fatalf("step failed: %v", err)
				}
			}
			if tx.Status() != tt.want {
				t.Fatalf("status = %s, want %s", tx.Status(), tt.want)
			}
		})
	}

	t.Run("terminal states accept no transition", func(t *testing.T) {
		for _, finish := range []step{process, reject, fail} {
			tx := newTx(t, KindRefund, "25.00")
			if err := finish(tx); err != nil {
				t.Fatal(err)
			}
			before := tx.Snapshot()
			for _, next := range []step{process, reject, fail, pending} {
				if err := next(tx); !errors.Is(err, ErrTerminalState) {
					t.Errorf("transition from %s error = %v, want ErrTerminalState", before.Status, err)
				}
			}
			after := tx.Snapshot()
			if after.Status != before.Status || after.FailureCode != before.FailureCode || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("terminal transaction changed: %+v -> %+v", before, after)
			}
		}
	})

	t.Run("pending_reference cannot be entered twice", func(t *testing.T) {
		tx := newTx(t, KindRefund, "25.00")
		_ = tx.MarkPendingReference(now)
		if err := tx.MarkPendingReference(now); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("error = %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("only reversals wait for a reference", func(t *testing.T) {
		if err := newTx(t, KindBet, "25.00").MarkPendingReference(now); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("error = %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("reversal needs the resolved reference to be processed", func(t *testing.T) {
		tx := newTx(t, KindRollback, "25.00")
		if err := tx.MarkProcessed(balance, nil, now); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("error = %v, want ErrInvalidTransition", err)
		}
		if tx.Status() != StatusPending {
			t.Errorf("status changed to %s", tx.Status())
		}
	})

	t.Run("rejection and failure require a code", func(t *testing.T) {
		tx := newTx(t, KindBet, "25.00")
		if err := tx.MarkRejected("", balance, now); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("MarkRejected without code error = %v", err)
		}
		if err := tx.MarkFailed("", now); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("MarkFailed without code error = %v", err)
		}
	})

	t.Run("rejected stores the balance observed at rejection", func(t *testing.T) {
		tx := newTx(t, KindBet, "80.00")
		observed := mustMoney(t, "20.00")
		if err := tx.MarkRejected("INSUFFICIENT_FUNDS", observed, now); err != nil {
			t.Fatal(err)
		}
		s := tx.Snapshot()
		if !s.BalanceAfter.Equal(observed) || s.FailureCode != "INSUFFICIENT_FUNDS" {
			t.Errorf("snapshot = %+v", s)
		}
	})

	t.Run("rejection requires a valid observed balance and leaves state untouched", func(t *testing.T) {
		tx := newTx(t, KindBet, "25.00")
		for _, invalid := range []Money{{}, mustNeg(t, mustMoney(t, "1.00"))} {
			if err := tx.MarkRejected("INSUFFICIENT_FUNDS", invalid, now); !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("error = %v, want ErrInvalidTransition", err)
			}
		}
		if err := tx.MarkRejected("", balance, now); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("error = %v, want ErrInvalidTransition", err)
		}
		if s := tx.Snapshot(); s.Status != StatusPending || s.BalanceAfter != nil || s.FailureCode != "" {
			t.Errorf("failed rejection changed state: %+v", s)
		}
	})

	t.Run("processed stores the observed balance", func(t *testing.T) {
		tx := newTx(t, KindBet, "25.00")
		later := now.Add(time.Minute)
		if err := tx.MarkProcessed(balance, nil, later); err != nil {
			t.Fatal(err)
		}
		s := tx.Snapshot()
		if !s.BalanceAfter.Equal(balance) || !s.ProcessedAt.Equal(later) {
			t.Errorf("snapshot = %+v", s)
		}
	})
}

func TestTransaction_SnapshotIsACopy(t *testing.T) {
	tx := newTx(t, KindBet, "25.00")
	_ = tx.MarkProcessed(mustMoney(t, "1.00"), nil, now)

	s := tx.Snapshot()
	s.External.ProviderID = "changed"
	*s.ProcessedAt = time.Time{}
	s.Status = StatusPending

	again := tx.Snapshot()
	if again.External.ProviderID != "provider-a" || again.ProcessedAt.IsZero() || again.Status != StatusProcessed {
		t.Fatalf("snapshot mutation leaked into the entity: %+v", again)
	}
}

func TestRehydrateWagerTransaction(t *testing.T) {
	tx := newTx(t, KindBet, "25.00")
	_ = tx.MarkRejected("INSUFFICIENT_FUNDS", mustMoney(t, "20.00"), now)

	back, err := RehydrateWagerTransaction(tx.Snapshot())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if back.Status() != StatusRejected || back.ID() != tx.ID() {
		t.Errorf("rehydrated = %+v", back.Snapshot())
	}

	invalid := map[string]func(*WagerTransactionSnapshot){
		"processed without balance":    func(s *WagerTransactionSnapshot) { s.Status = StatusProcessed; s.BalanceAfter = nil },
		"rejected without code":        func(s *WagerTransactionSnapshot) { s.FailureCode = "" },
		"rejected without balance":     func(s *WagerTransactionSnapshot) { s.BalanceAfter = nil },
		"terminal without processedAt": func(s *WagerTransactionSnapshot) { s.ProcessedAt = nil },
		"unknown status":               func(s *WagerTransactionSnapshot) { s.Status = "DONE" },
		"opening with external data":   func(s *WagerTransactionSnapshot) { s.Kind = KindOpening },
		"external without metadata":    func(s *WagerTransactionSnapshot) { s.External = nil },
		"negative amount":              func(s *WagerTransactionSnapshot) { s.Money = mustNeg(t, s.Money) },
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			s := tx.Snapshot()
			mutate(&s)
			if _, err := RehydrateWagerTransaction(s); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
