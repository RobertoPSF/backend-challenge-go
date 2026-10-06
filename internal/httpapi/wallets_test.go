package httpapi

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
)

func TestCursor_RoundTrip(t *testing.T) {
	in := app.LedgerCursor{CreatedAt: time.Date(2026, 9, 8, 12, 0, 0, 123456000, time.UTC), ID: uuid.New()}
	out, err := decodeCursor(encodeCursor(in))
	if err != nil {
		t.Fatal(err)
	}
	if !out.CreatedAt.Equal(in.CreatedAt) || out.ID != in.ID {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

func TestCursor_Empty(t *testing.T) {
	if c, err := decodeCursor(""); c != nil || err != nil {
		t.Fatalf("empty cursor = %v, %v", c, err)
	}
}

func TestCursor_RejectsGarbage(t *testing.T) {
	for _, raw := range []string{
		"%%%",
		base64.RawURLEncoding.EncodeToString([]byte("not json")),
		base64.RawURLEncoding.EncodeToString([]byte(`{"t":"2026-09-08T12:00:00Z"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"id":"` + uuid.NewString() + `"}`)),
	} {
		if _, err := decodeCursor(raw); !errors.Is(err, errInvalidRequest) {
			t.Errorf("decodeCursor(%q) error = %v", raw, err)
		}
	}
}

func TestParseLimit(t *testing.T) {
	valid := map[string]int{"": defaultLedgerLimit, "1": 1, "200": 200}
	for raw, want := range valid {
		if got, err := parseLimit(raw); err != nil || got != want {
			t.Errorf("parseLimit(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"0", "-1", "201", "abc", "1.5"} {
		if _, err := parseLimit(raw); !errors.Is(err, errInvalidRequest) {
			t.Errorf("parseLimit(%q) error = %v", raw, err)
		}
	}
}
