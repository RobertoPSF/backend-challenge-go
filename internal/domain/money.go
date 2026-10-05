package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type Currency struct {
	code string
}

var (
	BRL = Currency{code: "BRL"}
	USD = Currency{code: "USD"}
	EUR = Currency{code: "EUR"}

	supportedCurrencies = map[string]Currency{BRL.code: BRL, USD.code: USD, EUR.code: EUR}
)

func ParseCurrency(code string) (Currency, error) {
	c, ok := supportedCurrencies[code]
	if !ok {
		return Currency{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, code)
	}
	return c, nil
}

func (c Currency) String() string { return c.code }

func (c Currency) IsValid() bool { return c.code != "" }

const minorUnitsPerUnit = 100

var externalAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{2}$`)

type Money struct {
	minor    int64
	currency Currency
}

func ParseMoney(amount, currency string) (Money, error) {
	cur, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	if !externalAmountPattern.MatchString(amount) {
		return Money{}, fmt.Errorf("%w: %q must be a non-negative decimal with exactly 2 decimal places", ErrInvalidMoney, amount)
	}

	units, cents, _ := strings.Cut(amount, ".")
	u, err := strconv.ParseInt(units, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q out of range", ErrInvalidMoney, amount)
	}
	c, _ := strconv.ParseInt(cents, 10, 64)
	if u > (math.MaxInt64-c)/minorUnitsPerUnit {
		return Money{}, fmt.Errorf("%w: %q out of range", ErrInvalidMoney, amount)
	}
	return Money{minor: u*minorUnitsPerUnit + c, currency: cur}, nil
}

func NewMoney(minor int64, currency Currency) (Money, error) {
	if !currency.IsValid() {
		return Money{}, ErrInvalidCurrency
	}
	return Money{minor: minor, currency: currency}, nil
}

func Zero(currency Currency) Money {
	return Money{currency: currency}
}

func (m Money) MinorUnits() int64 { return m.minor }

func (m Money) Currency() Currency { return m.currency }

func (m Money) IsValid() bool { return m.currency.IsValid() }

func (m Money) IsZero() bool { return m.minor == 0 }

func (m Money) IsPositive() bool { return m.minor > 0 }

func (m Money) IsNegative() bool { return m.minor < 0 }

func (m Money) Add(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.minor > 0 && m.minor > math.MaxInt64-o.minor) || (o.minor < 0 && m.minor < math.MinInt64-o.minor) {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: m.minor + o.minor, currency: m.currency}, nil
}

func (m Money) Sub(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.minor < 0 && m.minor > math.MaxInt64+o.minor) || (o.minor > 0 && m.minor < math.MinInt64+o.minor) {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: m.minor - o.minor, currency: m.currency}, nil
}

func (m Money) Neg() (Money, error) {
	if !m.IsValid() {
		return Money{}, ErrInvalidMoney
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

func (m Money) Compare(o Money) (int, error) {
	if err := m.compatible(o); err != nil {
		return 0, err
	}
	switch {
	case m.minor < o.minor:
		return -1, nil
	case m.minor > o.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) Equal(o Money) bool {
	return m.currency == o.currency && m.minor == o.minor
}

func (m Money) String() string {
	magnitude := uint64(m.minor)
	sign := ""
	if m.minor < 0 {
		magnitude = ^magnitude + 1
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, magnitude/minorUnitsPerUnit, magnitude%minorUnitsPerUnit)
}

func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsValid() {
		return nil, ErrInvalidMoney
	}
	return json.Marshal(struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}{m.String(), m.currency.code})
}

func (m Money) compatible(o Money) error {
	if !m.IsValid() || !o.IsValid() {
		return ErrInvalidMoney
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}
