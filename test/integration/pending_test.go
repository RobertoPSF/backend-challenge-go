//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

var fastPending = config.Config{Pending: config.Pending{
	BaseBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, MaxAttempts: 4, TTL: time.Hour,
}}

func (e wagerEnv) drainPending(t *testing.T, w *app.Wagers, until time.Duration) {
	t.Helper()
	deadline := time.Now().Add(until)
	for time.Now().Before(deadline) {
		if _, err := w.ResumeNextPending(context.Background()); err != nil {
			t.Fatalf("resume: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e wagerEnv) tx(t *testing.T, id uuid.UUID) (status, failure string, attempts int) {
	t.Helper()
	var code *string
	if err := e.db.QueryRow(context.Background(), `SELECT status, failure_code, attempts FROM wager_transactions WHERE id = $1`, id).
		Scan(&status, &code, &attempts); err != nil {
		t.Fatal(err)
	}
	return status, deref(code), attempts
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestPendingReferences(t *testing.T) {
	env := newWagerEnvWith(t, 3, fastPending)
	id := func(prefix string) string { return prefix + "-" + uuid.NewString() }

	t.Run("REFUND that arrived first is applied by the worker after the BET", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		betID := id("bet")
		refund := env.process(t, refCmd(t, w, "REFUND", "30.00", id("refund"), betID))
		expectStatus(t, refund, domain.StatusPendingReference, "")

		env.drainPending(t, env.instances[1], 40*time.Millisecond)
		if status, _, attempts := env.tx(t, refund.ID); status != "PENDING_REFERENCE" || attempts == 0 {
			t.Fatalf("before the BET: %s attempts=%d, want still pending and retried", status, attempts)
		}

		env.process(t, wagerCmd(t, w, "BET", "30.00", betID))
		env.drainPending(t, env.instances[2], 100*time.Millisecond)

		if status, _, _ := env.tx(t, refund.ID); status != "PROCESSED" {
			t.Fatalf("refund status = %s, want PROCESSED", status)
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 10000 {
			t.Errorf("balance = %d, want 10000", balance)
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE payload->'data'->>'transactionId' = $1::text
			AND event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged')`, refund.ID); n != 2 {
			t.Errorf("events after resolution = %d, want 2", n)
		}
		env.assertLedgerMatchesBalance(t, w.ID())
	})

	t.Run("the arrival of the reference wakes the pending operation up", func(t *testing.T) {
		slow := newWagerEnvWith(t, 1, config.Config{Pending: config.Pending{
			BaseBackoff: time.Hour, MaxBackoff: time.Hour, MaxAttempts: 10, TTL: 2 * time.Hour,
		}})
		w := slow.openWallet(t, "100.00")
		betID := id("bet")
		refund := slow.process(t, refCmd(t, w, "REFUND", "30.00", id("refund"), betID))

		if found, _ := slow.instances[0].ResumeNextPending(context.Background()); found {
			t.Fatal("pending operation must not be due before its backoff")
		}
		slow.process(t, wagerCmd(t, w, "BET", "30.00", betID))
		if found, err := slow.instances[0].ResumeNextPending(context.Background()); !found || err != nil {
			t.Fatalf("after the BET the pending operation must be due immediately: found=%v err=%v", found, err)
		}
		if status, _, _ := slow.tx(t, refund.ID); status != "PROCESSED" {
			t.Errorf("refund = %s, want PROCESSED", status)
		}
	})

	t.Run("expires as REFERENCE_NOT_FOUND after the maximum attempts", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		refund := env.process(t, refCmd(t, w, "REFUND", "30.00", id("refund"), id("never")))
		env.drainPending(t, env.instances[0], 500*time.Millisecond)

		status, failure, attempts := env.tx(t, refund.ID)
		if status != "REJECTED" || failure != "REFERENCE_NOT_FOUND" || attempts != fastPending.Pending.MaxAttempts-1 {
			t.Fatalf("got %s %s attempts=%d", status, failure, attempts)
		}
		var observed int64
		_ = env.db.QueryRow(context.Background(), `SELECT balance_after FROM wager_transactions WHERE id = $1`, refund.ID).Scan(&observed)
		if observed != 10000 {
			t.Errorf("observed balance = %d, want 10000", observed)
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionRejected'
			AND payload->'data'->>'transactionId' = $1::text AND payload->'data'->>'failureCode' = 'REFERENCE_NOT_FOUND'`, refund.ID); n != 1 {
			t.Errorf("rejection events = %d", n)
		}
	})

	t.Run("expires when the TTL is over", func(t *testing.T) {
		short := newWagerEnvWith(t, 1, config.Config{Pending: config.Pending{
			BaseBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond, MaxAttempts: 1000, TTL: 60 * time.Millisecond,
		}})
		w := short.openWallet(t, "100.00")
		refund := short.process(t, refCmd(t, w, "ROLLBACK", "30.00", id("rollback"), id("never")))
		short.drainPending(t, short.instances[0], 300*time.Millisecond)
		if status, failure, attempts := short.tx(t, refund.ID); status != "REJECTED" || failure != "REFERENCE_NOT_FOUND" || attempts > 20 {
			t.Fatalf("got %s %s attempts=%d", status, failure, attempts)
		}
	})

	t.Run("reference that ends rejected rejects the waiting operation", func(t *testing.T) {
		w := env.openWallet(t, "10.00")
		betID := id("bet")
		refund := env.process(t, refCmd(t, w, "REFUND", "50.00", id("refund"), betID))
		expectStatus(t, env.process(t, wagerCmd(t, w, "BET", "50.00", betID)), domain.StatusRejected, "INSUFFICIENT_FUNDS")
		env.drainPending(t, env.instances[0], 100*time.Millisecond)
		if status, failure, _ := env.tx(t, refund.ID); status != "REJECTED" || failure != "REFERENCE_NOT_PROCESSED" {
			t.Fatalf("got %s %s", status, failure)
		}
	})

	t.Run("chained pending: ROLLBACK of a REFUND that is itself waiting", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		betID, refundID := id("bet"), id("refund")
		rollback := env.process(t, refCmd(t, w, "ROLLBACK", "30.00", id("rollback"), refundID))
		refund := env.process(t, refCmd(t, w, "REFUND", "30.00", refundID, betID))
		env.drainPending(t, env.instances[0], 60*time.Millisecond)
		if s, _, _ := env.tx(t, rollback.ID); s != "PENDING_REFERENCE" {
			t.Fatalf("rollback must wait while the refund is pending, got %s", s)
		}

		env.process(t, wagerCmd(t, w, "BET", "30.00", betID))
		env.drainPending(t, env.instances[0], 200*time.Millisecond)
		for _, txID := range []uuid.UUID{refund.ID, rollback.ID} {
			if s, f, _ := env.tx(t, txID); s != "PROCESSED" {
				t.Errorf("%s = %s %s, want PROCESSED", txID, s, f)
			}
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 7000 {
			t.Errorf("balance = %d, want 7000 (bet, refunded, refund rolled back)", balance)
		}
		env.assertLedgerMatchesBalance(t, w.ID())
	})

	t.Run("many workers across instances resolve each pending operation exactly once", func(t *testing.T) {
		w := env.openWallet(t, "1000.00")
		var pending []uuid.UUID
		var betIDs []string
		for range 20 {
			betID := id("bet")
			betIDs = append(betIDs, betID)
			pending = append(pending, env.process(t, refCmd(t, w, "REFUND", "10.00", id("refund"), betID)).ID)
		}
		for _, betID := range betIDs {
			env.process(t, wagerCmd(t, w, "BET", "10.00", betID))
		}

		var wg sync.WaitGroup
		for i := range 9 {
			wg.Go(func() { env.drainPending(t, env.instances[i%3], 300*time.Millisecond) })
		}
		wg.Wait()

		for _, txID := range pending {
			if s, f, _ := env.tx(t, txID); s != "PROCESSED" {
				t.Errorf("%s = %s %s", txID, s, f)
			}
		}
		if n := count(t, env.db, `SELECT count(*) FROM wallet_ledger_entries l JOIN wager_transactions t ON t.id = l.transaction_id
			WHERE l.wallet_id = $1 AND t.kind = 'REFUND'`, w.ID()); n != 20 {
			t.Errorf("refund credits = %d, want 20", n)
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 100000 {
			t.Errorf("balance = %d, want 100000", balance)
		}
		env.assertLedgerMatchesBalance(t, w.ID())
	})
}
