package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	testPlayerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	testWalletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
)

func validInput() WagerRequestInput {
	return WagerRequestInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		PlayerID:              testPlayerID,
		WalletID:              testWalletID,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 &MoneyInput{Amount: "25.00", Currency: "BRL"},
	}
}

func mustParse(t *testing.T, in WagerRequestInput, key string) WagerRequest {
	t.Helper()
	r, err := ParseWagerRequest(in, key)
	if err != nil {
		t.Fatalf("ParseWagerRequest: %v", err)
	}
	return r
}

func TestWagerRequest_CanonicalJSON(t *testing.T) {
	r := mustParse(t, validInput(), "provider-a:transaction-123")

	want := `{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET",` +
		`"money":{"amount":"25.00","currency":"BRL"},"playerId":"` + testPlayerID + `",` +
		`"providerId":"provider-a","roundId":"round-987","walletId":"` + testWalletID + `"}`
	if got := string(r.CanonicalJSON()); got != want {
		t.Fatalf("canonical JSON\n got: %s\nwant: %s", got, want)
	}
	const golden = "629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344"
	if r.PayloadHash() != golden {
		t.Errorf("hash = %s, want %s (sha256 of the canonical JSON above)", r.PayloadHash(), golden)
	}
}

func TestWagerRequest_ReferenceIsHashedOnlyWhenPresent(t *testing.T) {
	in := validInput()
	in.Kind = "REFUND"
	in.ReferenceExternalTransactionID = "transaction-100"
	r := mustParse(t, in, "k")
	if !strings.Contains(string(r.CanonicalJSON()), `"referenceExternalTransactionId":"transaction-100"`) {
		t.Errorf("reference missing from canonical JSON: %s", r.CanonicalJSON())
	}
	if strings.Contains(string(mustParse(t, validInput(), "k").CanonicalJSON()), "reference") {
		t.Error("absent reference must be omitted, not serialized as empty or null")
	}
}

func TestWagerRequest_HashIgnoresIdempotencyKeyAndTransport(t *testing.T) {
	a := mustParse(t, validInput(), "provider-a:transaction-123")
	b := mustParse(t, validInput(), "some-other-key")
	if a.PayloadHash() != b.PayloadHash() {
		t.Error("idempotency key must not affect the payload hash")
	}
	if a.ExternalDetails().IdempotencyKey != "provider-a:transaction-123" || a.ExternalDetails().PayloadHash != a.PayloadHash() {
		t.Errorf("external details = %+v", a.ExternalDetails())
	}
}

func TestWagerRequest_HashIsEqualForHTTPAndSQS(t *testing.T) {
	httpBody := `{
		"providerId": "provider-a",
		"externalTransactionId": "transaction-123",
		"playerId": "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1",
		"walletId": "` + testWalletID + `",
		"roundId": "round-987",
		"gameId": "fortune-chimp",
		"kind": "BET",
		"money": { "amount": "25.00", "currency": "BRL" }
	}`
	var fromHTTP WagerRequestInput
	if err := json.Unmarshal([]byte(httpBody), &fromHTTP); err != nil {
		t.Fatal(err)
	}

	sqsMessage := `{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",
		"data":{"money":{"currency":"BRL","amount":"25.00"},"kind":"BET","gameId":"fortune-chimp","roundId":"round-987",
		"walletId":"` + testWalletID + `","playerId":"` + testPlayerID + `","idempotencyKey":"provider-a:transaction-123",
		"externalTransactionId":"transaction-123","providerId":"provider-a"}}`
	var envelope struct {
		MessageID string `json:"messageId"`
		Data      struct {
			WagerRequestInput
			IdempotencyKey string `json:"idempotencyKey"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(sqsMessage), &envelope); err != nil {
		t.Fatal(err)
	}

	viaHTTP := mustParse(t, fromHTTP, "provider-a:transaction-123")
	viaSQS := mustParse(t, envelope.Data.WagerRequestInput, envelope.Data.IdempotencyKey)
	if viaHTTP.PayloadHash() != viaSQS.PayloadHash() {
		t.Fatalf("HTTP and SQS hashes differ:\n%s\n%s", viaHTTP.CanonicalJSON(), viaSQS.CanonicalJSON())
	}
}

func TestWagerRequest_EveryBusinessFieldChangesTheHash(t *testing.T) {
	base := mustParse(t, validInput(), "k").PayloadHash()
	changes := map[string]func(*WagerRequestInput){
		"providerId":            func(in *WagerRequestInput) { in.ProviderID = "provider-b" },
		"externalTransactionId": func(in *WagerRequestInput) { in.ExternalTransactionID = "transaction-124" },
		"playerId":              func(in *WagerRequestInput) { in.PlayerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2" },
		"walletId":              func(in *WagerRequestInput) { in.WalletID = "0192f291-27dd-7d3f-8071-5f8685deef38" },
		"roundId":               func(in *WagerRequestInput) { in.RoundID = "round-988" },
		"gameId":                func(in *WagerRequestInput) { in.GameID = "other-game" },
		"kind":                  func(in *WagerRequestInput) { in.Kind = "WIN" },
		"amount":                func(in *WagerRequestInput) { in.Money = &MoneyInput{Amount: "25.01", Currency: "BRL"} },
		"currency":              func(in *WagerRequestInput) { in.Money = &MoneyInput{Amount: "25.00", Currency: "USD"} },
		"reference": func(in *WagerRequestInput) {
			in.Kind = "WIN"
			in.ReferenceExternalTransactionID = "bet-1"
		},
	}
	seen := map[string]string{base: "base"}
	for field, change := range changes {
		in := validInput()
		change(&in)
		h := mustParse(t, in, "k").PayloadHash()
		if other, dup := seen[h]; dup {
			t.Errorf("changing %s produced the same hash as %s", field, other)
		}
		seen[h] = field
	}
}

func TestWagerRequest_UUIDCaseIsNormalized(t *testing.T) {
	upper := validInput()
	upper.PlayerID = strings.ToUpper(testPlayerID)
	upper.WalletID = strings.ToUpper(testWalletID)

	a, b := mustParse(t, validInput(), "k"), mustParse(t, upper, "k")
	if a.PayloadHash() != b.PayloadHash() || a.PlayerID != b.PlayerID {
		t.Fatal("upper and lower case UUIDs must be the same request")
	}
	if !strings.Contains(string(b.CanonicalJSON()), testPlayerID) {
		t.Errorf("UUIDs must be hashed in lower case: %s", b.CanonicalJSON())
	}
}

func TestParseWagerRequest_RejectsInvalidInput(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*WagerRequestInput)
		key     string
		wantErr error
	}{
		"empty provider":           {func(in *WagerRequestInput) { in.ProviderID = "" }, "k", ErrInvalidTransaction},
		"provider with space":      {func(in *WagerRequestInput) { in.ProviderID = "provider a" }, "k", ErrInvalidTransaction},
		"provider with padding":    {func(in *WagerRequestInput) { in.ProviderID = " provider-a" }, "k", ErrInvalidTransaction},
		"non ascii external id":    {func(in *WagerRequestInput) { in.ExternalTransactionID = "transação-1" }, "k", ErrInvalidTransaction},
		"external id too long":     {func(in *WagerRequestInput) { in.ExternalTransactionID = strings.Repeat("x", 129) }, "k", ErrInvalidTransaction},
		"slash in round":           {func(in *WagerRequestInput) { in.RoundID = "round/1" }, "k", ErrInvalidTransaction},
		"empty game":               {func(in *WagerRequestInput) { in.GameID = "" }, "k", ErrInvalidTransaction},
		"invalid reference":        {func(in *WagerRequestInput) { in.ReferenceExternalTransactionID = "ref 1" }, "k", ErrInvalidTransaction},
		"missing idempotency key":  {func(in *WagerRequestInput) {}, "", ErrInvalidTransaction},
		"key with space":           {func(in *WagerRequestInput) {}, "provider-a: tx", ErrInvalidTransaction},
		"key too long":             {func(in *WagerRequestInput) {}, strings.Repeat("k", 257), ErrInvalidTransaction},
		"player not a uuid":        {func(in *WagerRequestInput) { in.PlayerID = "player-1" }, "k", ErrInvalidTransaction},
		"braced uuid":              {func(in *WagerRequestInput) { in.PlayerID = "{" + testPlayerID + "}" }, "k", ErrInvalidTransaction},
		"urn uuid":                 {func(in *WagerRequestInput) { in.WalletID = "urn:uuid:" + testWalletID }, "k", ErrInvalidTransaction},
		"uuid without hyphens":     {func(in *WagerRequestInput) { in.WalletID = strings.ReplaceAll(testWalletID, "-", "") }, "k", ErrInvalidTransaction},
		"nil uuid":                 {func(in *WagerRequestInput) { in.WalletID = "00000000-0000-0000-0000-000000000000" }, "k", ErrInvalidTransaction},
		"unknown kind":             {func(in *WagerRequestInput) { in.Kind = "CASHOUT" }, "k", ErrInvalidTransaction},
		"lowercase kind":           {func(in *WagerRequestInput) { in.Kind = "bet" }, "k", ErrInvalidTransaction},
		"opening kind":             {func(in *WagerRequestInput) { in.Kind = "OPENING" }, "k", ErrUnsupportedKind},
		"missing money":            {func(in *WagerRequestInput) { in.Money = nil }, "k", ErrInvalidTransaction},
		"invalid amount":           {func(in *WagerRequestInput) { in.Money = &MoneyInput{Amount: "25", Currency: "BRL"} }, "k", ErrInvalidMoney},
		"invalid currency":         {func(in *WagerRequestInput) { in.Money = &MoneyInput{Amount: "25.00", Currency: "brl"} }, "k", ErrInvalidCurrency},
		"key allows provider:txid": {func(in *WagerRequestInput) {}, "provider-a:transaction-123", nil},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			tt.mutate(&in)
			_, err := ParseWagerRequest(in, tt.key)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestWagerRequestInput_AmountMustBeAJSONString(t *testing.T) {
	var in WagerRequestInput
	err := json.Unmarshal([]byte(`{"money":{"amount":25.00,"currency":"BRL"}}`), &in)
	if err == nil {
		t.Fatal("numeric amount must not decode into the input (no float on the wire)")
	}
}
