package domain

type ErrorKind string

const (
	KindValidation ErrorKind = "VALIDATION"
	KindBusiness   ErrorKind = "BUSINESS"
	KindConflict   ErrorKind = "CONFLICT"
	KindInternal   ErrorKind = "INTERNAL"
)

type FailureCode string

type DomainError struct {
	Kind    ErrorKind
	Code    FailureCode
	Message string
}

func (e *DomainError) Error() string {
	return string(e.Code) + ": " + e.Message
}

func newError(kind ErrorKind, code FailureCode, message string) *DomainError {
	return &DomainError{Kind: kind, Code: code, Message: message}
}

var (
	ErrInvalidMoney       = newError(KindValidation, "INVALID_MONEY", "invalid monetary value")
	ErrInvalidCurrency    = newError(KindValidation, "INVALID_CURRENCY", "unsupported or invalid currency")
	ErrInvalidAmount      = newError(KindValidation, "INVALID_AMOUNT", "amount not allowed for this operation")
	ErrInvalidTransaction = newError(KindValidation, "INVALID_REQUEST", "invalid transaction data")
	ErrUnsupportedKind    = newError(KindValidation, "UNSUPPORTED_KIND", "transaction kind not accepted from external sources")

	ErrCurrencyMismatch  = newError(KindBusiness, "CURRENCY_MISMATCH", "currencies do not match")
	ErrInsufficientFunds = newError(KindBusiness, "INSUFFICIENT_FUNDS", "insufficient funds")
	ErrMoneyOverflow     = newError(KindBusiness, "AMOUNT_OUT_OF_RANGE", "monetary value out of range")

	ErrInvalidWallet      = newError(KindInternal, "INVALID_WALLET", "invalid wallet state")
	ErrInvalidLedgerEntry = newError(KindInternal, "INVALID_LEDGER_ENTRY", "invalid ledger entry")
	ErrInvalidTransition  = newError(KindInternal, "INVALID_TRANSITION", "invalid transaction state transition")
	ErrTerminalState      = newError(KindInternal, "TERMINAL_STATE", "transaction is in a terminal state")
	ErrInvalidEvent       = newError(KindInternal, "INVALID_EVENT", "invalid event data")
)
