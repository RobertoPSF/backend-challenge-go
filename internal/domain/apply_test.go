package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func txFor(t *testing.T, w *Wallet, kind Kind, amount string) *WagerTransaction {
	t.Helper()
	tx, err := NewExternalTransaction(kind, w.ID(), w.PlayerID(), mustMoney(t, amount), validExternal(""), now)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestApplyToWallet(t *testing.T) {
	tests := []struct {
		kind        Kind
		amount      string
		wantBalance string
		wantVersion int64
		wantEntry   Direction
	}{
		{KindBet, "30.00", "70.00", 2, DirectionDebit},
		{KindBet, "100.00", "0.00", 2, DirectionDebit},
		{KindWin, "30.00", "130.00", 2, DirectionCredit},
		{KindLoss, "0.00", "100.00", 1, ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind)+"_"+tt.amount, func(t *testing.T) {
			w := newTestWallet(t, "100.00")
			entry, err := ApplyToWallet(w, txFor(t, w, tt.kind, tt.amount), nil, false, now)
			if err != nil {
				t.Fatal(err)
			}
			if w.Balance().String() != tt.wantBalance || w.Version() != tt.wantVersion {
				t.Errorf("wallet = %s v%d, want %s v%d", w.Balance(), w.Version(), tt.wantBalance, tt.wantVersion)
			}
			if tt.wantEntry == "" && entry != nil {
				t.Errorf("LOSS must not produce a ledger entry: %+v", entry)
			}
			if tt.wantEntry != "" && (entry == nil || entry.Direction() != tt.wantEntry) {
				t.Errorf("entry = %+v, want %s", entry, tt.wantEntry)
			}
		})
	}
}

func TestApplyToWallet_Rejections(t *testing.T) {
	w := newTestWallet(t, "10.00")
	anotherPlayer, _ := NewExternalTransaction(KindBet, w.ID(), uuid.New(), mustMoney(t, "1.00"), validExternal(""), now)
	usd, _ := ParseMoney("0.00", "USD")
	otherCurrency, _ := NewExternalTransaction(KindLoss, w.ID(), w.PlayerID(), usd, validExternal(""), now)

	tests := map[string]struct {
		tx   *WagerTransaction
		want error
	}{
		"bet above balance":        {txFor(t, w, KindBet, "10.01"), ErrInsufficientFunds},
		"another player":           {anotherPlayer, ErrPlayerWalletMismatch},
		"loss in another currency": {otherCurrency, ErrCurrencyMismatch},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ApplyToWallet(w, tt.tx, nil, false, now)
			var de *DomainError
			if !errors.Is(err, tt.want) || !errors.As(err, &de) || de.Kind != KindBusiness {
				t.Fatalf("error = %v, want business error %v", err, tt.want)
			}
			if w.Balance().String() != "10.00" || w.Version() != 1 {
				t.Errorf("wallet changed: %s v%d", w.Balance(), w.Version())
			}
		})
	}
}

func externalWith(id, ref string) ExternalDetails {
	e := validExternal(ref)
	e.ExternalTransactionID = id
	e.IdempotencyKey = "provider-a:" + id
	return e
}

func processedRef(t *testing.T, w *Wallet, kind Kind, amount string) *WagerTransaction {
	t.Helper()
	ref := ""
	if kind.IsReversal() {
		ref = "earlier"
	}
	tx, err := NewExternalTransaction(kind, w.ID(), w.PlayerID(), mustMoney(t, amount), externalWith("ref-"+string(kind), ref), now)
	if err != nil {
		t.Fatal(err)
	}
	refID := tx.ID()
	if err := tx.MarkProcessed(mustMoney(t, "0.00"), &refID, now); err != nil {
		t.Fatal(err)
	}
	return tx
}

func referencing(t *testing.T, w *Wallet, kind Kind, amount string, ref *WagerTransaction) *WagerTransaction {
	t.Helper()
	tx, err := NewExternalTransaction(kind, w.ID(), w.PlayerID(), mustMoney(t, amount),
		externalWith("op-"+string(kind), ref.Snapshot().External.ExternalTransactionID), now)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestApplyToWallet_ReferencedOperations(t *testing.T) {
	tests := []struct {
		name        string
		kind        Kind
		refKind     Kind
		balance     string
		wantBalance string
		wantDir     Direction
	}{
		{"REFUND of a BET credits", KindRefund, KindBet, "70.00", "100.00", DirectionCredit},
		{"ROLLBACK of a BET credits", KindRollback, KindBet, "70.00", "100.00", DirectionCredit},
		{"ROLLBACK of a WIN debits", KindRollback, KindWin, "130.00", "100.00", DirectionDebit},
		{"ROLLBACK of a REFUND debits", KindRollback, KindRefund, "130.00", "100.00", DirectionDebit},
		{"WIN referencing a BET credits", KindWin, KindBet, "70.00", "100.00", DirectionCredit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newTestWallet(t, tt.balance)
			ref := processedRef(t, w, tt.refKind, "30.00")
			entry, err := ApplyToWallet(w, referencing(t, w, tt.kind, "30.00", ref), ref, false, now)
			if err != nil {
				t.Fatal(err)
			}
			if w.Balance().String() != tt.wantBalance || entry.Direction() != tt.wantDir {
				t.Errorf("balance %s dir %s, want %s %s", w.Balance(), entry.Direction(), tt.wantBalance, tt.wantDir)
			}
		})
	}
}

func TestApplyToWallet_ReferencedRejections(t *testing.T) {
	type tc struct {
		name            string
		kind            Kind
		refKind         Kind
		amount          string
		balance         string
		alreadyReversed bool
		mutateRef       func(*WagerTransactionSnapshot)
		want            error
	}
	tests := []tc{
		{name: "REFUND of a WIN", kind: KindRefund, refKind: KindWin, amount: "30.00", balance: "100.00", want: ErrReferenceKindNotReversible},
		{name: "ROLLBACK of a LOSS", kind: KindRollback, refKind: KindLoss, amount: "0.01", balance: "100.00", want: ErrReferenceKindNotReversible},
		{name: "ROLLBACK of a ROLLBACK", kind: KindRollback, refKind: KindRollback, amount: "30.00", balance: "100.00", want: ErrReferenceKindNotReversible},
		{name: "partial REFUND", kind: KindRefund, refKind: KindBet, amount: "10.00", balance: "100.00", want: ErrAmountMismatch},
		{name: "second REFUND", kind: KindRefund, refKind: KindBet, amount: "30.00", balance: "100.00", alreadyReversed: true, want: ErrAlreadyReversed},
		{name: "ROLLBACK of a refunded BET", kind: KindRollback, refKind: KindBet, amount: "30.00", balance: "100.00", alreadyReversed: true, want: ErrAlreadyReversed},
		{name: "ROLLBACK of a WIN without funds", kind: KindRollback, refKind: KindWin, amount: "30.00", balance: "29.99", want: ErrReversalInsufficientFunds},
		{name: "WIN referencing a WIN", kind: KindWin, refKind: KindWin, amount: "30.00", balance: "100.00", want: ErrReferenceMismatch},
		{name: "reference rejected", kind: KindRefund, refKind: KindBet, amount: "30.00", balance: "100.00",
			mutateRef: func(s *WagerTransactionSnapshot) { s.Status = StatusRejected; s.FailureCode = "INSUFFICIENT_FUNDS" }, want: ErrReferenceNotProcessed},
		{name: "different round", kind: KindRefund, refKind: KindBet, amount: "30.00", balance: "100.00",
			mutateRef: func(s *WagerTransactionSnapshot) { s.External.RoundID = "round-other" }, want: ErrReferenceMismatch},
		{name: "different wallet", kind: KindRefund, refKind: KindBet, amount: "30.00", balance: "100.00",
			mutateRef: func(s *WagerTransactionSnapshot) { s.WalletID = newID() }, want: ErrReferenceMismatch},
		{name: "different player", kind: KindRefund, refKind: KindBet, amount: "30.00", balance: "100.00",
			mutateRef: func(s *WagerTransactionSnapshot) { s.PlayerID = newID() }, want: ErrReferenceMismatch},
		{name: "different provider", kind: KindRefund, refKind: KindBet, amount: "30.00", balance: "100.00",
			mutateRef: func(s *WagerTransactionSnapshot) { s.External.ProviderID = "provider-b" }, want: ErrReferenceMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newTestWallet(t, tt.balance)
			refAmount := tt.amount
			if tt.want == ErrAmountMismatch {
				refAmount = "30.00"
			}
			if tt.refKind == KindLoss {
				refAmount = "0.00"
			}
			ref := processedRef(t, w, tt.refKind, refAmount)
			if tt.mutateRef != nil {
				s := ref.Snapshot()
				tt.mutateRef(&s)
				var err error
				if ref, err = RehydrateWagerTransaction(s); err != nil {
					t.Fatal(err)
				}
			}
			_, err := ApplyToWallet(w, referencing(t, w, tt.kind, tt.amount, ref), ref, tt.alreadyReversed, now)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if w.Balance().String() != tt.balance || w.Version() != 1 {
				t.Errorf("wallet changed: %s v%d", w.Balance(), w.Version())
			}
		})
	}
}
