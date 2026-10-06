package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

var (
	identifierPattern     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)
	uuidPattern           = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type MoneyInput struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type WagerRequestInput struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       string      `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          *MoneyInput `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}

type WagerRequest struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          Money
	ReferenceExternalTransactionID string
}

func ParseWagerRequest(in WagerRequestInput, idempotencyKey string) (WagerRequest, error) {
	identifiers := []struct{ field, value string }{
		{"providerId", in.ProviderID},
		{"externalTransactionId", in.ExternalTransactionID},
		{"roundId", in.RoundID},
		{"gameId", in.GameID},
	}
	for _, id := range identifiers {
		if !identifierPattern.MatchString(id.value) {
			return WagerRequest{}, fmt.Errorf("%w: %s must match %s", ErrInvalidTransaction, id.field, identifierPattern)
		}
	}
	if in.ReferenceExternalTransactionID != "" && !identifierPattern.MatchString(in.ReferenceExternalTransactionID) {
		return WagerRequest{}, fmt.Errorf("%w: referenceExternalTransactionId must match %s", ErrInvalidTransaction, identifierPattern)
	}
	if !idempotencyKeyPattern.MatchString(idempotencyKey) {
		return WagerRequest{}, fmt.Errorf("%w: idempotency key must match %s", ErrInvalidTransaction, idempotencyKeyPattern)
	}

	playerID, err := parseUUID("playerId", in.PlayerID)
	if err != nil {
		return WagerRequest{}, err
	}
	walletID, err := parseUUID("walletId", in.WalletID)
	if err != nil {
		return WagerRequest{}, err
	}
	kind, err := ParseExternalKind(in.Kind)
	if err != nil {
		return WagerRequest{}, err
	}
	if in.Money == nil {
		return WagerRequest{}, fmt.Errorf("%w: money is required", ErrInvalidTransaction)
	}
	money, err := ParseMoney(in.Money.Amount, in.Money.Currency)
	if err != nil {
		return WagerRequest{}, err
	}

	return WagerRequest{
		ProviderID:                     in.ProviderID,
		ExternalTransactionID:          in.ExternalTransactionID,
		IdempotencyKey:                 idempotencyKey,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        in.RoundID,
		GameID:                         in.GameID,
		Kind:                           kind,
		Money:                          money,
		ReferenceExternalTransactionID: in.ReferenceExternalTransactionID,
	}, nil
}

func parseUUID(field, raw string) (uuid.UUID, error) {
	if !uuidPattern.MatchString(raw) {
		return uuid.Nil, fmt.Errorf("%w: %s must be a UUID in canonical 8-4-4-4-12 form", ErrInvalidTransaction, field)
	}
	id := uuid.MustParse(raw)
	if id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s cannot be the nil UUID", ErrInvalidTransaction, field)
	}
	return id, nil
}

func (r WagerRequest) CanonicalJSON() []byte {
	fields := map[string]any{
		"providerId":            r.ProviderID,
		"externalTransactionId": r.ExternalTransactionID,
		"playerId":              r.PlayerID.String(),
		"walletId":              r.WalletID.String(),
		"roundId":               r.RoundID,
		"gameId":                r.GameID,
		"kind":                  string(r.Kind),
		"money": map[string]string{
			"amount":   r.Money.String(),
			"currency": r.Money.Currency().String(),
		},
	}
	if r.ReferenceExternalTransactionID != "" {
		fields["referenceExternalTransactionId"] = r.ReferenceExternalTransactionID
	}
	raw, _ := json.Marshal(fields)
	return raw
}

func (r WagerRequest) PayloadHash() string {
	sum := sha256.Sum256(r.CanonicalJSON())
	return hex.EncodeToString(sum[:])
}

func (r WagerRequest) ExternalDetails() ExternalDetails {
	return ExternalDetails{
		ProviderID:                     r.ProviderID,
		ExternalTransactionID:          r.ExternalTransactionID,
		IdempotencyKey:                 r.IdempotencyKey,
		PayloadHash:                    r.PayloadHash(),
		RoundID:                        r.RoundID,
		GameID:                         r.GameID,
		ReferenceExternalTransactionID: r.ReferenceExternalTransactionID,
	}
}
