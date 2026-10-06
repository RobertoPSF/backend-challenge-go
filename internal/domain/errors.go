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
	ErrWalletNotFound     = newError(KindValidation, "WALLET_NOT_FOUND", "wallet does not exist")

	ErrWalletAlreadyExists         = newError(KindConflict, "WALLET_ALREADY_EXISTS", "player already has a wallet in this currency")
	ErrIdempotencyKeyConflict      = newError(KindConflict, "IDEMPOTENCY_KEY_CONFLICT", "idempotency key already used with a different payload")
	ErrExternalTransactionConflict = newError(KindConflict, "EXTERNAL_TRANSACTION_CONFLICT", "external transaction already registered with another idempotency key")

	ErrCurrencyMismatch     = newError(KindBusiness, "CURRENCY_MISMATCH", "currencies do not match")
	ErrInsufficientFunds    = newError(KindBusiness, "INSUFFICIENT_FUNDS", "insufficient funds")
	ErrPlayerWalletMismatch = newError(KindBusiness, "PLAYER_WALLET_MISMATCH", "wallet does not belong to the player")

	ErrReferenceNotFound          = newError(KindBusiness, "REFERENCE_NOT_FOUND", "referenced transaction did not arrive in time")
	ErrReferenceNotProcessed      = newError(KindBusiness, "REFERENCE_NOT_PROCESSED", "referenced transaction was not processed successfully")
	ErrReferenceMismatch          = newError(KindBusiness, "REFERENCE_MISMATCH", "operation does not match the referenced transaction")
	ErrReferenceKindNotReversible = newError(KindBusiness, "REFERENCE_KIND_NOT_REVERSIBLE", "referenced transaction kind cannot be reversed this way")
	ErrAmountMismatch             = newError(KindBusiness, "AMOUNT_MISMATCH", "amount differs from the referenced transaction")
	ErrAlreadyReversed            = newError(KindBusiness, "ALREADY_REVERSED", "referenced transaction already has a successful reversal")
	ErrReversalInsufficientFunds  = newError(KindBusiness, "REVERSAL_INSUFFICIENT_FUNDS", "insufficient funds to reverse the referenced transaction")
	ErrMoneyOverflow              = newError(KindBusiness, "AMOUNT_OUT_OF_RANGE", "monetary value out of range")

	ErrInvalidWallet      = newError(KindInternal, "INVALID_WALLET", "invalid wallet state")
	ErrInvalidLedgerEntry = newError(KindInternal, "INVALID_LEDGER_ENTRY", "invalid ledger entry")
	ErrInvalidTransition  = newError(KindInternal, "INVALID_TRANSITION", "invalid transaction state transition")
	ErrTerminalState      = newError(KindInternal, "TERMINAL_STATE", "transaction is in a terminal state")
	ErrInvalidEvent       = newError(KindInternal, "INVALID_EVENT", "invalid event data")
)
