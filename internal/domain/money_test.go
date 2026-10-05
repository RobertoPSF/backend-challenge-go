package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func mustMoney(t *testing.T, amount string) Money {
	t.Helper()
	m, err := ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatalf("ParseMoney(%q): %v", amount, err)
	}
	return m
}

func minor(t *testing.T, units int64, c Currency) Money {
	t.Helper()
	m, err := NewMoney(units, c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseMoney_Valid(t *testing.T) {
	tests := map[string]int64{
		"0.00":                 0,
		"0.01":                 1,
		"25.00":                2500,
		"1000.50":              100050,
		"92233720368547758.07": math.MaxInt64,
		"92233720368547758.00": 9223372036854775800,
		"12345678901234.99":    1234567890123499,
	}
	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			m, err := ParseMoney(in, "BRL")
			if err != nil {
				t.Fatalf("ParseMoney(%q) error = %v", in, err)
			}
			if m.MinorUnits() != want || m.Currency() != BRL {
				t.Errorf("ParseMoney(%q) = %d %s, want %d BRL", in, m.MinorUnits(), m.Currency(), want)
			}
			if m.String() != in {
				t.Errorf("String() = %q, want round trip %q", m.String(), in)
			}
		})
	}
}

func TestParseMoney_RejectsInvalidAmounts(t *testing.T) {
	inputs := []string{
		"", " ", "25", "25.0", "25.000", "25.", ".50", "-25.00", "+25.00", "025.00", "00.00",
		"25,00", "1e3", "1E3", "2.5e1", "NaN", "nan", "Infinity", "-Infinity", "Inf",
		" 25.00", "25.00 ", "0x10.00", "1_000.00", "２５.００",
		"92233720368547758.08", "99999999999999999999.99",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			_, err := ParseMoney(in, "BRL")
			if !errors.Is(err, ErrInvalidMoney) {
				t.Fatalf("ParseMoney(%q) error = %v, want ErrInvalidMoney", in, err)
			}
		})
	}
}

func TestParseMoney_RejectsInvalidCurrencies(t *testing.T) {
	for _, c := range []string{"", "brl", "Brl", "XYZ", "JPY", "BRLL", " BRL"} {
		t.Run(c, func(t *testing.T) {
			_, err := ParseMoney("1.00", c)
			if !errors.Is(err, ErrInvalidCurrency) {
				t.Fatalf("ParseMoney currency %q error = %v, want ErrInvalidCurrency", c, err)
			}
		})
	}
}

func TestMoney_ZeroValueIsInvalid(t *testing.T) {
	var zero Money
	if zero.IsValid() {
		t.Fatal("zero Money must be invalid")
	}
	valid := mustMoney(t, "1.00")
	if _, err := zero.Add(valid); !errors.Is(err, ErrInvalidMoney) {
		t.Errorf("Add with zero value error = %v, want ErrInvalidMoney", err)
	}
	if _, err := valid.Sub(zero); !errors.Is(err, ErrInvalidMoney) {
		t.Errorf("Sub with zero value error = %v, want ErrInvalidMoney", err)
	}
	if _, err := zero.Neg(); !errors.Is(err, ErrInvalidMoney) {
		t.Errorf("Neg of zero value error = %v, want ErrInvalidMoney", err)
	}
	if _, err := zero.Compare(valid); !errors.Is(err, ErrInvalidMoney) {
		t.Errorf("Compare with zero value error = %v, want ErrInvalidMoney", err)
	}
	if _, err := json.Marshal(zero); err == nil {
		t.Error("Marshal of zero value must fail")
	}
	if _, err := NewMoney(100, Currency{}); !errors.Is(err, ErrInvalidCurrency) {
		t.Errorf("NewMoney without currency error = %v, want ErrInvalidCurrency", err)
	}
	if Zero(Currency{}).IsValid() {
		t.Error("Zero of an invalid currency must be invalid")
	}
}

func TestMoney_Arithmetic(t *testing.T) {
	a, b := mustMoney(t, "100.00"), mustMoney(t, "80.00")

	sum, err := a.Add(b)
	if err != nil || sum.String() != "180.00" {
		t.Errorf("100.00 + 80.00 = %v, %v", sum, err)
	}
	diff, err := b.Sub(a)
	if err != nil || diff.String() != "-20.00" || !diff.IsNegative() {
		t.Errorf("80.00 - 100.00 = %v, %v; want -20.00", diff, err)
	}
	neg, err := a.Neg()
	if err != nil || neg.String() != "-100.00" {
		t.Errorf("Neg(100.00) = %v, %v", neg, err)
	}
	back, err := neg.Neg()
	if err != nil || !back.Equal(a) {
		t.Errorf("Neg(Neg(x)) = %v, want %v", back, a)
	}
	if !Zero(BRL).IsZero() || Zero(BRL).String() != "0.00" {
		t.Error("Zero(BRL) must be 0.00 BRL")
	}

	for _, tc := range []struct {
		x, y Money
		want int
	}{{a, b, 1}, {b, a, -1}, {a, a, 0}} {
		got, err := tc.x.Compare(tc.y)
		if err != nil || got != tc.want {
			t.Errorf("Compare(%s, %s) = %d, %v; want %d", tc.x, tc.y, got, err, tc.want)
		}
	}
}

func TestMoney_IsImmutable(t *testing.T) {
	a, b := mustMoney(t, "10.00"), mustMoney(t, "5.00")
	_, _ = a.Add(b)
	_, _ = a.Sub(b)
	_, _ = a.Neg()
	if a.String() != "10.00" || b.String() != "5.00" {
		t.Fatalf("operands changed: a=%s b=%s", a, b)
	}
}

func TestMoney_Overflow(t *testing.T) {
	maxM := minor(t, math.MaxInt64, BRL)
	minM := minor(t, math.MinInt64, BRL)
	one := minor(t, 1, BRL)

	if _, err := maxM.Add(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("MaxInt64 + 1 error = %v, want ErrMoneyOverflow", err)
	}
	if _, err := minM.Sub(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("MinInt64 - 1 error = %v, want ErrMoneyOverflow", err)
	}
	negOne, _ := one.Neg()
	if _, err := maxM.Sub(negOne); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("MaxInt64 - (-1) error = %v, want ErrMoneyOverflow", err)
	}
	if _, err := minM.Add(negOne); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("MinInt64 + (-1) error = %v, want ErrMoneyOverflow", err)
	}
	if _, err := minM.Neg(); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("Neg(MinInt64) error = %v, want ErrMoneyOverflow", err)
	}
	if got, err := maxM.Sub(one); err != nil || got.MinorUnits() != math.MaxInt64-1 {
		t.Errorf("MaxInt64 - 1 = %v, %v", got, err)
	}
	if minM.String() != "-92233720368547758.08" {
		t.Errorf("String(MinInt64) = %s", minM.String())
	}
}

func TestMoney_CurrencyMismatch(t *testing.T) {
	brl, usd := minor(t, 100, BRL), minor(t, 100, USD)

	checks := map[string]error{}
	_, checks["Add"] = brl.Add(usd)
	_, checks["Sub"] = brl.Sub(usd)
	_, checks["Compare"] = brl.Compare(usd)
	for op, err := range checks {
		if !errors.Is(err, ErrCurrencyMismatch) {
			t.Errorf("%s(BRL, USD) error = %v, want ErrCurrencyMismatch", op, err)
		}
	}
	if brl.Equal(usd) {
		t.Error("100 BRL must not equal 100 USD")
	}
}

func TestMoney_JSON(t *testing.T) {
	m := mustMoney(t, "25.00")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Errorf("Marshal = %s", b)
	}

	neg, _ := m.Neg()
	b, _ = json.Marshal(neg)
	if string(b) != `{"amount":"-25.00","currency":"BRL"}` {
		t.Errorf("Marshal negative = %s", b)
	}
}

func TestDomainError_Classification(t *testing.T) {
	_, err := ParseMoney("1.5", "BRL")

	var de *DomainError
	if !errors.As(err, &de) {
		t.Fatalf("error %v is not a *DomainError", err)
	}
	if de.Kind != KindValidation || de.Code != "INVALID_MONEY" {
		t.Errorf("got kind=%s code=%s", de.Kind, de.Code)
	}
}
