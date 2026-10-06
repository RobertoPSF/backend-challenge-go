//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/test/testinfra"
)

type wagerEnv struct {
	instances []*app.Wagers
	envs      []storeEnv
	wallets   *app.Wallets
	db        *pgx.Conn
}

func newWagerEnv(t *testing.T, instances int) wagerEnv {
	t.Helper()
	pg := testinfra.StartPostgres(t)
	env := wagerEnv{db: connect(t, pg.OwnerURL)}
	for range instances {
		se := newStoreOn(t, pg.AppURL, 5*time.Second)
		env.envs = append(env.envs, se)
		env.instances = append(env.instances, app.NewWagers(se.store))
	}
	env.wallets = app.NewWallets(env.envs[0].store)
	return env
}

func (e wagerEnv) openWallet(t *testing.T, amount string) *domain.Wallet {
	t.Helper()
	w, err := e.wallets.Open(context.Background(), uuid.New(), brl(t, amount), "corr-test")
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func wagerCmd(t *testing.T, w *domain.Wallet, kind, amount, externalID string) app.WagerCommand {
	t.Helper()
	in := domain.WagerRequestInput{
		ProviderID: "provider-a", ExternalTransactionID: externalID,
		PlayerID: w.PlayerID().String(), WalletID: w.ID().String(),
		RoundID: "round-1", GameID: "game-1", Kind: kind,
		Money: &domain.MoneyInput{Amount: amount, Currency: w.Currency().String()},
	}
	req, err := domain.ParseWagerRequest(in, "provider-a:"+externalID)
	if err != nil {
		t.Fatal(err)
	}
	return app.WagerCommand{Request: req, CorrelationID: "corr-" + externalID}
}

func (e wagerEnv) walletState(t *testing.T, id uuid.UUID) (balance int64, version int64) {
	t.Helper()
	if err := e.db.QueryRow(context.Background(), `SELECT balance, version FROM wallets WHERE id = $1`, id).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	return balance, version
}

func (e wagerEnv) assertLedgerMatchesBalance(t *testing.T, walletID uuid.UUID) {
	t.Helper()
	var stored, calculated int64
	err := e.db.QueryRow(context.Background(), `SELECT w.balance,
		COALESCE((SELECT sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END) FROM wallet_ledger_entries WHERE wallet_id = w.id), 0)
		FROM wallets w WHERE w.id = $1`, walletID).Scan(&stored, &calculated)
	if err != nil {
		t.Fatal(err)
	}
	if stored != calculated {
		t.Fatalf("stored balance %d != ledger credits - debits %d", stored, calculated)
	}
}

func TestProcessWager(t *testing.T) {
	env := newWagerEnv(t, 1)
	wagers := env.instances[0]
	ctx := context.Background()

	t.Run("BET debits the wallet and records ledger and events atomically", func(t *testing.T) {
		w := env.openWallet(t, "1000.00")
		res, err := wagers.Process(ctx, wagerCmd(t, w, "BET", "25.00", "bet-"+uuid.NewString()))
		if err != nil {
			t.Fatal(err)
		}
		s := res.Transaction.Snapshot()
		if res.Replay || s.Status != domain.StatusProcessed || s.BalanceAfter.String() != "975.00" {
			t.Fatalf("result = %+v replay=%v", s, res.Replay)
		}
		if balance, version := env.walletState(t, w.ID()); balance != 97500 || version != 2 {
			t.Errorf("wallet = %d v%d, want 97500 v2", balance, version)
		}
		if n := count(t, env.db, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1 AND direction = 'DEBIT'`, s.ID); n != 1 {
			t.Errorf("ledger entries = %d", n)
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE payload->'data'->>'transactionId' = $1::text`, s.ID); n != 2 {
			t.Errorf("events = %d, want Processed + BalanceChanged", n)
		}
		env.assertLedgerMatchesBalance(t, w.ID())
	})

	t.Run("WIN credits the wallet", func(t *testing.T) {
		w := env.openWallet(t, "10.00")
		res, err := wagers.Process(ctx, wagerCmd(t, w, "WIN", "40.00", "win-"+uuid.NewString()))
		if err != nil || res.Transaction.Snapshot().BalanceAfter.String() != "50.00" {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		env.assertLedgerMatchesBalance(t, w.ID())
	})

	t.Run("LOSS is processed without ledger, version change or balance event", func(t *testing.T) {
		w := env.openWallet(t, "10.00")
		res, err := wagers.Process(ctx, wagerCmd(t, w, "LOSS", "0.00", "loss-"+uuid.NewString()))
		if err != nil || res.Transaction.Status() != domain.StatusProcessed {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if _, version := env.walletState(t, w.ID()); version != 1 {
			t.Errorf("version = %d, want 1", version)
		}
		id := res.Transaction.ID()
		if n := count(t, env.db, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, id); n != 0 {
			t.Errorf("ledger entries = %d", n)
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE payload->'data'->>'transactionId' = $1::text AND event_type = 'WagerTransactionProcessed'`, id); n != 1 {
			t.Errorf("processed events = %d", n)
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE payload->'data'->>'transactionId' = $1::text AND event_type = 'WalletBalanceChanged'`, id); n != 0 {
			t.Errorf("balance events = %d", n)
		}
	})

	rejections := map[string]struct {
		cmd  func(w *domain.Wallet) app.WagerCommand
		code domain.FailureCode
	}{
		"insufficient funds": {func(w *domain.Wallet) app.WagerCommand {
			return wagerCmd(t, w, "BET", "10.01", "bet-"+uuid.NewString())
		}, "INSUFFICIENT_FUNDS"},
		"player does not own the wallet": {func(w *domain.Wallet) app.WagerCommand {
			c := wagerCmd(t, w, "BET", "1.00", "bet-"+uuid.NewString())
			c.Request.PlayerID = uuid.New()
			return c
		}, "PLAYER_WALLET_MISMATCH"},
		"currency differs from the wallet": {func(w *domain.Wallet) app.WagerCommand {
			c := wagerCmd(t, w, "BET", "1.00", "bet-"+uuid.NewString())
			c.Request.Money, _ = domain.NewMoney(100, domain.USD)
			return c
		}, "CURRENCY_MISMATCH"},
	}
	for name, tc := range rejections {
		t.Run("rejected: "+name, func(t *testing.T) {
			w := env.openWallet(t, "10.00")
			res, err := wagers.Process(ctx, tc.cmd(w))
			if err != nil {
				t.Fatal(err)
			}
			s := res.Transaction.Snapshot()
			if s.Status != domain.StatusRejected || s.FailureCode != tc.code || s.BalanceAfter.String() != "10.00" {
				t.Fatalf("result = %s %s %v", s.Status, s.FailureCode, s.BalanceAfter)
			}
			if balance, version := env.walletState(t, w.ID()); balance != 1000 || version != 1 {
				t.Errorf("wallet changed: %d v%d", balance, version)
			}
			if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE payload->'data'->>'transactionId' = $1::text AND event_type = 'WagerTransactionRejected'`, s.ID); n != 1 {
				t.Errorf("rejected events = %d", n)
			}
		})
	}

	t.Run("unknown wallet is not persisted", func(t *testing.T) {
		w := env.openWallet(t, "10.00")
		c := wagerCmd(t, w, "BET", "1.00", "bet-ghost")
		c.Request.WalletID = uuid.New()
		if _, err := wagers.Process(ctx, c); !errors.Is(err, domain.ErrWalletNotFound) {
			t.Fatalf("err = %v, want ErrWalletNotFound", err)
		}
		if n := count(t, env.db, `SELECT count(*) FROM wager_transactions WHERE external_transaction_id = 'bet-ghost'`); n != 0 {
			t.Errorf("persisted rows = %d", n)
		}
	})

	t.Run("replay returns the original result and balance", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		cmd := wagerCmd(t, w, "BET", "30.00", "bet-"+uuid.NewString())
		first, err := wagers.Process(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wagers.Process(ctx, wagerCmd(t, w, "BET", "50.00", "bet-"+uuid.NewString())); err != nil {
			t.Fatal(err)
		}

		again, err := wagers.Process(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if !again.Replay || again.Transaction.ID() != first.Transaction.ID() || again.Transaction.Snapshot().BalanceAfter.String() != "70.00" {
			t.Fatalf("replay = %+v (replay=%v), want original balance 70.00", again.Transaction.Snapshot(), again.Replay)
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 2000 {
			t.Errorf("balance = %d, want 2000 (replay must not debit again)", balance)
		}
	})

	t.Run("replay of a rejection returns the rejection", func(t *testing.T) {
		w := env.openWallet(t, "5.00")
		cmd := wagerCmd(t, w, "BET", "50.00", "bet-"+uuid.NewString())
		_, _ = wagers.Process(ctx, cmd)
		again, err := wagers.Process(ctx, cmd)
		if err != nil || !again.Replay || again.Transaction.Snapshot().FailureCode != "INSUFFICIENT_FUNDS" {
			t.Fatalf("replay = %+v, err = %v", again, err)
		}
	})

	t.Run("same key with a different payload conflicts", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		cmd := wagerCmd(t, w, "BET", "10.00", "bet-"+uuid.NewString())
		_, _ = wagers.Process(ctx, cmd)
		changed := cmd
		changed.Request.Money = brl(t, "11.00")
		if _, err := wagers.Process(ctx, changed); !errors.Is(err, domain.ErrIdempotencyKeyConflict) {
			t.Fatalf("err = %v, want ErrIdempotencyKeyConflict", err)
		}
	})

	t.Run("same external transaction with another key conflicts", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		cmd := wagerCmd(t, w, "BET", "10.00", "bet-"+uuid.NewString())
		_, _ = wagers.Process(ctx, cmd)
		otherKey := cmd
		otherKey.Request.IdempotencyKey = "another-key-" + uuid.NewString()
		if _, err := wagers.Process(ctx, otherKey); !errors.Is(err, domain.ErrExternalTransactionConflict) {
			t.Fatalf("err = %v, want ErrExternalTransactionConflict", err)
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 9000 {
			t.Errorf("balance = %d, want 9000", balance)
		}
	})
}

func TestProcessWager_Concurrency(t *testing.T) {
	env := newWagerEnv(t, 3)
	ctx := context.Background()

	t.Run("the same bet sent 50 times in parallel debits once", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		cmd := wagerCmd(t, w, "BET", "10.00", "bet-"+uuid.NewString())

		results := runParallel(t, 50, func(i int) (app.WagerResult, error) {
			return env.instances[i%3].Process(ctx, cmd)
		})
		var processed, replays int
		ids := map[uuid.UUID]bool{}
		for _, r := range results {
			if r.err != nil {
				t.Fatalf("unexpected error: %v", r.err)
			}
			ids[r.res.Transaction.ID()] = true
			if r.res.Replay {
				replays++
			} else {
				processed++
			}
		}
		if processed != 1 || replays != 49 || len(ids) != 1 {
			t.Fatalf("processed=%d replays=%d distinct ids=%d; want 1, 49, 1", processed, replays, len(ids))
		}
		if n := count(t, env.db, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, w.ID()); n != 1 {
			t.Errorf("debits = %d, want 1", n)
		}
		if balance, version := env.walletState(t, w.ID()); balance != 9000 || version != 2 {
			t.Errorf("wallet = %d v%d", balance, version)
		}
		env.assertLedgerMatchesBalance(t, w.ID())
	})

	t.Run("two different 80.00 bets on 100.00: one processed, one rejected", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		cmds := []app.WagerCommand{
			wagerCmd(t, w, "BET", "80.00", "bet-a-"+uuid.NewString()),
			wagerCmd(t, w, "BET", "80.00", "bet-b-"+uuid.NewString()),
		}
		results := runParallel(t, 2, func(i int) (app.WagerResult, error) {
			return env.instances[i].Process(ctx, cmds[i])
		})

		statuses := map[domain.Status]int{}
		for _, r := range results {
			if r.err != nil {
				t.Fatalf("unexpected error: %v", r.err)
			}
			s := r.res.Transaction.Snapshot()
			statuses[s.Status]++
			if s.Status == domain.StatusRejected && (s.FailureCode != "INSUFFICIENT_FUNDS" || s.BalanceAfter.String() != "20.00") {
				t.Errorf("rejection = %s observed %s", s.FailureCode, s.BalanceAfter)
			}
		}
		if statuses[domain.StatusProcessed] != 1 || statuses[domain.StatusRejected] != 1 {
			t.Fatalf("statuses = %v", statuses)
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 2000 {
			t.Errorf("balance = %d, want 2000", balance)
		}
		if n := count(t, env.db, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, w.ID()); n != 1 {
			t.Errorf("debits = %d, want 1", n)
		}

		for i, cmd := range cmds {
			again, err := env.instances[i].Process(ctx, cmd)
			if err != nil || !again.Replay {
				t.Fatalf("resend %d = %+v, %v", i, again, err)
			}
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 2000 {
			t.Errorf("balance after resend = %d, want 2000", balance)
		}
		env.assertLedgerMatchesBalance(t, w.ID())
	})

	t.Run("many distinct bets on the same wallet never deadlock or lose updates", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		results := runParallel(t, 30, func(i int) (app.WagerResult, error) {
			return env.instances[i%3].Process(ctx, wagerCmd(t, w, "BET", "1.00", fmt.Sprintf("bet-%d-%s", i, uuid.NewString())))
		})
		for _, r := range results {
			if r.err != nil {
				t.Fatalf("unexpected error: %v", r.err)
			}
		}
		if balance, version := env.walletState(t, w.ID()); balance != 7000 || version != 31 {
			t.Errorf("wallet = %d v%d, want 7000 v31", balance, version)
		}
		env.assertLedgerMatchesBalance(t, w.ID())
		for _, se := range env.envs {
			if d := metricValue(t, se.reg, "wallet_concurrency_conflicts_total", "reason", "deadlock"); d != 0 {
				t.Errorf("deadlocks retried = %v, want 0", d)
			}
		}
	})

	t.Run("different wallets are processed in parallel", func(t *testing.T) {
		wallets := make([]*domain.Wallet, 20)
		for i := range wallets {
			wallets[i] = env.openWallet(t, "100.00")
		}
		results := runParallel(t, 200, func(i int) (app.WagerResult, error) {
			w := wallets[i%len(wallets)]
			return env.instances[i%3].Process(ctx, wagerCmd(t, w, "BET", "1.00", fmt.Sprintf("bet-%d-%s", i, uuid.NewString())))
		})
		for _, r := range results {
			if r.err != nil {
				t.Fatalf("unexpected error: %v", r.err)
			}
		}
		for _, w := range wallets {
			if balance, version := env.walletState(t, w.ID()); balance != 9000 || version != 11 {
				t.Errorf("wallet %s = %d v%d, want 9000 v11", w.ID(), balance, version)
			}
			env.assertLedgerMatchesBalance(t, w.ID())
		}
	})
}

type parallelResult struct {
	res app.WagerResult
	err error
}

func runParallel(t *testing.T, n int, fn func(i int) (app.WagerResult, error)) []parallelResult {
	t.Helper()
	results := make([]parallelResult, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			res, err := fn(i)
			results[i] = parallelResult{res: res, err: err}
		})
	}
	close(start)
	wg.Wait()
	return results
}
