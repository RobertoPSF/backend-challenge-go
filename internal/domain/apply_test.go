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
			entry, err := ApplyToWallet(w, txFor(t, w, tt.kind, tt.amount), now)
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
			_, err := ApplyToWallet(w, tt.tx, now)
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
