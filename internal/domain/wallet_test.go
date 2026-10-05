package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var now = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func newTestWallet(t *testing.T, balance string) *Wallet {
	t.Helper()
	w, err := RehydrateWallet(uuid.New(), uuid.New(), mustMoney(t, balance), InitialWalletVersion, now, now)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWallet_DebitAndCredit(t *testing.T) {
	w := newTestWallet(t, "100.00")
	txID := uuid.New()

	entry, err := w.Debit(txID, mustMoney(t, "80.00"), now.Add(time.Second))
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if w.Balance().String() != "20.00" || w.Version() != 2 {
		t.Errorf("after debit: balance=%s version=%d, want 20.00 v2", w.Balance(), w.Version())
	}
	if entry.Direction() != DirectionDebit || entry.BalanceBefore().String() != "100.00" ||
		entry.BalanceAfter().String() != "20.00" || entry.Amount().String() != "80.00" ||
		entry.TransactionID() != txID || entry.WalletID() != w.ID() {
		t.Errorf("unexpected entry: %+v", entry)
	}
	if !w.UpdatedAt().Equal(now.Add(time.Second)) {
		t.Errorf("updatedAt = %s", w.UpdatedAt())
	}

	if _, err := w.Credit(uuid.New(), mustMoney(t, "5.50"), now); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if w.Balance().String() != "25.50" || w.Version() != 3 {
		t.Errorf("after credit: balance=%s version=%d, want 25.50 v3", w.Balance(), w.Version())
	}
}

func TestWallet_DebitDownToZeroIsAllowed(t *testing.T) {
	w := newTestWallet(t, "80.00")
	if _, err := w.Debit(uuid.New(), mustMoney(t, "80.00"), now); err != nil {
		t.Fatalf("Debit of the whole balance: %v", err)
	}
	if !w.Balance().IsZero() {
		t.Errorf("balance = %s, want 0.00", w.Balance())
	}
}

func TestWallet_RejectedMovementsLeaveStateUntouched(t *testing.T) {
	usd, _ := NewMoney(100, USD)
	tests := map[string]struct {
		apply   func(w *Wallet) error
		wantErr error
	}{
		"insufficient funds": {func(w *Wallet) error { _, err := w.Debit(uuid.New(), mustMoney(t, "20.01"), now); return err }, ErrInsufficientFunds},
		"currency mismatch":  {func(w *Wallet) error { _, err := w.Credit(uuid.New(), usd, now); return err }, ErrCurrencyMismatch},
		"zero credit":        {func(w *Wallet) error { _, err := w.Credit(uuid.New(), Zero(BRL), now); return err }, ErrInvalidAmount},
		"zero debit":         {func(w *Wallet) error { _, err := w.Debit(uuid.New(), Zero(BRL), now); return err }, ErrInvalidAmount},
		"invalid money":      {func(w *Wallet) error { _, err := w.Debit(uuid.New(), Money{}, now); return err }, ErrInvalidMoney},
		"missing tx id":      {func(w *Wallet) error { _, err := w.Credit(uuid.Nil, mustMoney(t, "1.00"), now); return err }, ErrInvalidLedgerEntry},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			w := newTestWallet(t, "20.00")
			if err := tt.apply(w); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if w.Balance().String() != "20.00" || w.Version() != 1 || !w.UpdatedAt().Equal(now) {
				t.Errorf("state changed after rejection: balance=%s version=%d", w.Balance(), w.Version())
			}
		})
	}
}

func TestWallet_CreditOverflow(t *testing.T) {
	maxBalance, _ := NewMoney(1<<63-1, BRL)
	w, err := RehydrateWallet(uuid.New(), uuid.New(), maxBalance, 1, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Credit(uuid.New(), mustMoney(t, "0.01"), now); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("error = %v, want ErrMoneyOverflow", err)
	}
}

func TestRehydrateWallet_ValidatesStateWithoutSideEffects(t *testing.T) {
	balance := mustMoney(t, "10.00")
	negative, _ := balance.Neg()

	w, err := RehydrateWallet(uuid.New(), uuid.New(), balance, 7, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.Version() != 7 || w.Balance().String() != "10.00" {
		t.Errorf("rehydrate must keep persisted state as is: v=%d balance=%s", w.Version(), w.Balance())
	}

	invalid := map[string]func() (*Wallet, error){
		"nil id":           func() (*Wallet, error) { return RehydrateWallet(uuid.Nil, uuid.New(), balance, 1, now, now) },
		"nil player":       func() (*Wallet, error) { return RehydrateWallet(uuid.New(), uuid.Nil, balance, 1, now, now) },
		"negative balance": func() (*Wallet, error) { return RehydrateWallet(uuid.New(), uuid.New(), negative, 1, now, now) },
		"invalid balance":  func() (*Wallet, error) { return RehydrateWallet(uuid.New(), uuid.New(), Money{}, 1, now, now) },
		"version zero":     func() (*Wallet, error) { return RehydrateWallet(uuid.New(), uuid.New(), balance, 0, now, now) },
		"no timestamps":    func() (*Wallet, error) { return RehydrateWallet(uuid.New(), uuid.New(), balance, 1, time.Time{}, now) },
	}
	for name, build := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := build(); !errors.Is(err, ErrInvalidWallet) {
				t.Fatalf("error = %v, want ErrInvalidWallet", err)
			}
		})
	}
}

func TestWallet_UninitializedIsRejected(t *testing.T) {
	var w Wallet
	if _, err := w.Credit(uuid.New(), mustMoney(t, "1.00"), now); !errors.Is(err, ErrInvalidWallet) {
		t.Fatalf("error = %v, want ErrInvalidWallet", err)
	}
}

func TestLedgerEntry_ValidatesBalanceMath(t *testing.T) {
	m := func(s string) Money { return mustMoney(t, s) }
	walletID, txID := uuid.New(), uuid.New()

	valid := []struct {
		dir                   Direction
		amount, before, after Money
	}{
		{DirectionCredit, m("10.00"), m("0.00"), m("10.00")},
		{DirectionDebit, m("10.00"), m("10.00"), m("0.00")},
	}
	for _, tc := range valid {
		if _, err := NewLedgerEntry(walletID, txID, tc.dir, tc.amount, tc.before, tc.after, now); err != nil {
			t.Errorf("%s %s: unexpected error %v", tc.dir, tc.amount, err)
		}
	}

	usd, _ := NewMoney(1000, USD)
	invalid := map[string]struct {
		dir                   Direction
		amount, before, after Money
	}{
		"credit math wrong":     {DirectionCredit, m("10.00"), m("0.00"), m("9.99")},
		"debit math wrong":      {DirectionDebit, m("10.00"), m("10.00"), m("10.00")},
		"debit below zero":      {DirectionDebit, m("10.00"), m("5.00"), mustNeg(t, m("5.00"))},
		"zero amount":           {DirectionCredit, m("0.00"), m("1.00"), m("1.00")},
		"unknown direction":     {Direction("SIDEWAYS"), m("1.00"), m("1.00"), m("2.00")},
		"currency mismatch":     {DirectionCredit, usd, m("0.00"), m("10.00")},
		"invalid balance after": {DirectionCredit, m("1.00"), m("0.00"), Money{}},
	}
	for name, tc := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := NewLedgerEntry(walletID, txID, tc.dir, tc.amount, tc.before, tc.after, now); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func mustNeg(t *testing.T, m Money) Money {
	t.Helper()
	n, err := m.Neg()
	if err != nil {
		t.Fatal(err)
	}
	return n
}
