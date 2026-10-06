package app

import (
	"testing"
	"time"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

func TestBackoff_GrowsExponentiallyUpToTheCapWithBoundedJitter(t *testing.T) {
	s := &Wagers{pending: config.Pending{BaseBackoff: time.Second, MaxBackoff: 30 * time.Second}}
	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		30 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for attempts, base := range want {
		for range 50 {
			got := s.backoff(attempts)
			if got < base || got > base+base/10 {
				t.Fatalf("backoff(%d) = %s, want between %s and %s", attempts, got, base, base+base/10)
			}
		}
	}
	if got := s.backoff(1000); got < 30*time.Second || got > 33*time.Second {
		t.Fatalf("backoff(1000) = %s, want capped at 30s", got)
	}
}

func TestBackoff_LargeBaseDoesNotOverflow(t *testing.T) {
	s := &Wagers{pending: config.Pending{BaseBackoff: 24 * time.Hour, MaxBackoff: 48 * time.Hour}}
	for _, attempts := range []int{0, 1, 40, 1 << 20} {
		if got := s.backoff(attempts); got <= 0 || got > 48*time.Hour+48*time.Hour/10 {
			t.Fatalf("backoff(%d) = %s", attempts, got)
		}
	}
}
