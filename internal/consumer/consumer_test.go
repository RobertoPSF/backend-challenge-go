package consumer

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

const validMessage = `{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",
	"data":{"providerId":"provider-a","externalTransactionId":"transaction-123","idempotencyKey":"provider-a:transaction-123",
	"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",
	"roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}`

func TestDecode_ReadmeExample(t *testing.T) {
	env, err := decode(validMessage)
	if err != nil {
		t.Fatal(err)
	}
	if env.MessageID != "msg-123" || env.Data.IdempotencyKey != "provider-a:transaction-123" || env.Data.Money.Amount != "25.00" {
		t.Fatalf("decoded = %+v", env)
	}
	if _, err := domain.ParseWagerRequest(env.Data.WagerRequestInput, env.Data.IdempotencyKey); err != nil {
		t.Fatalf("README example must be a valid request: %v", err)
	}
}

func TestDecode_RejectsInvalidEnvelopes(t *testing.T) {
	cases := map[string]string{
		"not json":           `{"messageId":`,
		"two objects":        validMessage + `{}`,
		"missing message id": strings.Replace(validMessage, `"messageId":"msg-123",`, ``, 1),
		"wrong type":         strings.Replace(validMessage, `WagerTransactionRequested`, `SomethingElse`, 1),
		"missing occurredAt": strings.Replace(validMessage, `"occurredAt":"2026-09-08T12:00:00.000Z",`, ``, 1),
		"missing data":       `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"x"}`,
		"unknown field":      strings.Replace(validMessage, `"type":`, `"extra":1,"type":`, 1),
		"numeric amount":     strings.Replace(validMessage, `"amount":"25.00"`, `"amount":25.00`, 1),
	}
	for name, body := range cases {
		if _, err := decode(body); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		err  error
		want outcome
		code string
	}{
		{fmt.Errorf("x: %w", domain.ErrInvalidMoney), outcomeDeadLetter, "INVALID_MONEY"},
		{domain.ErrWalletNotFound, outcomeDeadLetter, "WALLET_NOT_FOUND"},
		{domain.ErrIdempotencyKeyConflict, outcomeDeadLetter, "IDEMPOTENCY_KEY_CONFLICT"},
		{domain.ErrMessageIDConflict, outcomeDeadLetter, "MESSAGE_ID_CONFLICT"},
		{fmt.Errorf("%w: boom", app.ErrUnavailable), outcomeRetry, ""},
		{errors.New("unexpected"), outcomeRetry, ""},
	}
	for _, c := range cases {
		got, reason := decide(c.err)
		if got != c.want || (c.code != "" && reason != c.code) {
			t.Errorf("decide(%v) = %v %q, want %v %q", c.err, got, reason, c.want, c.code)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	c := &Consumer{cfg: config.Consumer{RetryBaseDelay: 2 * time.Second, RetryMaxDelay: 20 * time.Second}}
	want := map[int]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 3: 8 * time.Second, 4: 16 * time.Second, 5: 20 * time.Second, 50: 20 * time.Second}
	for count, d := range want {
		if got := c.retryDelay(count); got != d {
			t.Errorf("retryDelay(%d) = %s, want %s", count, got, d)
		}
	}
}
