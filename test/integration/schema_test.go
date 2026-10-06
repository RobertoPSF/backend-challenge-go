//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/RobertoPSF/backend-challenge-go/test/testinfra"
)

const (
	pgUniqueViolation     = "23505"
	pgCheckViolation      = "23514"
	pgForeignKeyViolation = "23503"
	pgIntegrityViolation  = "23000"
	pgInsufficientPriv    = "42501"
)

func TestMigrations_UpDownUp(t *testing.T) {
	pg := testinfra.StartPostgres(t)

	if err := testinfra.MigrateDownAll(pg.OwnerURL); err != nil {
		t.Fatalf("down: %v", err)
	}
	owner := connect(t, pg.OwnerURL)
	var tables int
	_ = owner.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`).Scan(&tables)
	if tables != 0 {
		t.Fatalf("tables after down = %d, want 0", tables)
	}

	if err := testinfra.MigrateUp(pg.OwnerURL); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestSchema_Invariants(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	app := connect(t, pg.AppURL)
	owner := connect(t, pg.OwnerURL)

	t.Run("wallet balance cannot be negative", func(t *testing.T) {
		row := walletRow()
		row["balance"] = -1
		expectViolation(t, insert(app, "wallets", row), pgCheckViolation, "wallets_balance_check")
	})

	t.Run("one wallet per player and currency", func(t *testing.T) {
		row := walletRow()
		mustExec(t, insert(app, "wallets", row))
		row["id"] = uuid.New()
		expectViolation(t, insert(app, "wallets", row), pgUniqueViolation, "wallets_player_currency_uk")
	})

	t.Run("app role cannot delete financial records", func(t *testing.T) {
		w := newWallet(t, app)
		tx := newTransaction(t, app, w, nil)
		mustExec(t, insert(app, "wallet_ledger_entries", ledgerRow(w, tx, nil)))

		for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "outbox_events", "inbox_messages"} {
			_, err := app.Exec(context.Background(), "DELETE FROM "+table)
			expectCode(t, err, pgInsufficientPriv)
		}
	})

	t.Run("ledger is append-only for the app role", func(t *testing.T) {
		w := newWallet(t, app)
		tx := newTransaction(t, app, w, nil)
		mustExec(t, insert(app, "wallet_ledger_entries", ledgerRow(w, tx, nil)))

		for _, stmt := range []string{
			"UPDATE wallet_ledger_entries SET amount = 1",
			"DELETE FROM wallet_ledger_entries",
			"TRUNCATE wallet_ledger_entries",
		} {
			_, err := app.Exec(context.Background(), stmt)
			expectCode(t, err, pgInsufficientPriv)
		}
	})

	t.Run("ledger is append-only even for the schema owner", func(t *testing.T) {
		w := newWallet(t, app)
		tx := newTransaction(t, app, w, nil)
		mustExec(t, insert(app, "wallet_ledger_entries", ledgerRow(w, tx, nil)))

		for _, stmt := range []string{
			"UPDATE wallet_ledger_entries SET created_at = now()",
			"DELETE FROM wallet_ledger_entries",
			"TRUNCATE wallet_ledger_entries CASCADE",
		} {
			_, err := owner.Exec(context.Background(), stmt)
			expectCode(t, err, pgIntegrityViolation)
		}
	})

	t.Run("ledger enforces balance math", func(t *testing.T) {
		w := newWallet(t, app)
		tx := newTransaction(t, app, w, nil)
		expectViolation(t, insert(app, "wallet_ledger_entries", ledgerRow(w, tx, map[string]any{"balance_after": 9_999})),
			pgCheckViolation, "ledger_balance_math_ck")
		expectViolation(t, insert(app, "wallet_ledger_entries", ledgerRow(w, tx, map[string]any{
			"direction": "DEBIT", "balance_before": 1_000, "balance_after": 1_000,
		})), pgCheckViolation, "ledger_balance_math_ck")
		expectViolation(t, insert(app, "wallet_ledger_entries", ledgerRow(w, tx, map[string]any{
			"direction": "DEBIT", "balance_before": 400, "balance_after": -100,
		})), pgCheckViolation, "wallet_ledger_entries_balance_after_check")
	})

	t.Run("one ledger entry per wallet and transaction", func(t *testing.T) {
		w := newWallet(t, app)
		tx := newTransaction(t, app, w, nil)
		mustExec(t, insert(app, "wallet_ledger_entries", ledgerRow(w, tx, nil)))
		expectViolation(t, insert(app, "wallet_ledger_entries", ledgerRow(w, tx, nil)), pgUniqueViolation, "ledger_wallet_transaction_uk")
	})

	t.Run("ledger entry must belong to the transaction's wallet", func(t *testing.T) {
		w, other := newWallet(t, app), newWallet(t, app)
		tx := newTransaction(t, app, w, nil)
		expectViolation(t, insert(app, "wallet_ledger_entries", ledgerRow(other, tx, nil)), pgForeignKeyViolation, "ledger_transaction_belongs_to_wallet_fk")
	})

	t.Run("idempotency key and external id are unique per provider", func(t *testing.T) {
		w := newWallet(t, app)
		first := txRow(w, map[string]any{"idempotency_key": "provider-a:dup", "external_transaction_id": "dup"})
		mustExec(t, insert(app, "wager_transactions", first))

		sameKey := txRow(w, map[string]any{"idempotency_key": "provider-a:dup"})
		expectViolation(t, insert(app, "wager_transactions", sameKey), pgUniqueViolation, "wt_provider_idempotency_key_uk")

		sameExternalID := txRow(w, map[string]any{"external_transaction_id": "dup"})
		expectViolation(t, insert(app, "wager_transactions", sameExternalID), pgUniqueViolation, "wt_provider_external_id_uk")

		otherProvider := txRow(w, map[string]any{"provider_id": "provider-b", "idempotency_key": "provider-a:dup", "external_transaction_id": "dup"})
		mustExec(t, insert(app, "wager_transactions", otherProvider))
	})

	t.Run("only one opening per wallet", func(t *testing.T) {
		w := newWallet(t, app)
		mustExec(t, insert(app, "wager_transactions", openingRow(w)))
		expectViolation(t, insert(app, "wager_transactions", openingRow(w)), pgUniqueViolation, "wt_single_opening_per_wallet_uk")
	})

	t.Run("internal and external origins are distinguished", func(t *testing.T) {
		w := newWallet(t, app)
		openingWithProvider := openingRow(w)
		openingWithProvider["provider_id"] = "provider-a"
		expectViolation(t, insert(app, "wager_transactions", openingWithProvider), pgCheckViolation, "wt_internal_has_no_external_data_ck")

		externalOpening := txRow(w, map[string]any{"kind": "OPENING"})
		expectViolation(t, insert(app, "wager_transactions", externalOpening), pgCheckViolation, "wt_origin_matches_kind_ck")

		for _, column := range []string{"provider_id", "external_transaction_id", "idempotency_key", "payload_hash", "round_id", "game_id"} {
			for _, missing := range []any{nil, ""} {
				row := txRow(w, map[string]any{column: missing})
				expectViolation(t, insert(app, "wager_transactions", row), pgCheckViolation, "wt_external_has_required_data_ck")
			}
		}
	})

	t.Run("amount policy by kind", func(t *testing.T) {
		w := newWallet(t, app)
		expectViolation(t, insert(app, "wager_transactions", txRow(w, map[string]any{"kind": "BET", "amount": 0})), pgCheckViolation, "wt_amount_by_kind_ck")
		expectViolation(t, insert(app, "wager_transactions", txRow(w, map[string]any{"kind": "LOSS", "amount": 100})), pgCheckViolation, "wt_amount_by_kind_ck")
		mustExec(t, insert(app, "wager_transactions", txRow(w, map[string]any{"kind": "LOSS", "amount": 0})))
	})

	t.Run("reference rules", func(t *testing.T) {
		w := newWallet(t, app)
		for _, missing := range []any{nil, ""} {
			row := txRow(w, map[string]any{"kind": "REFUND", "status": "PENDING", "processed_at": nil, "balance_after": nil,
				"balance_currency": nil, "reference_external_transaction_id": missing})
			expectViolation(t, insert(app, "wager_transactions", row), pgCheckViolation, "wt_reversal_has_reference_ck")
		}
		expectViolation(t, insert(app, "wager_transactions", txRow(w, map[string]any{"kind": "BET", "reference_external_transaction_id": "x"})),
			pgCheckViolation, "wt_bet_loss_have_no_reference_ck")
		mustExec(t, insert(app, "wager_transactions", txRow(w, map[string]any{"kind": "WIN", "reference_external_transaction_id": "x"})))
	})

	t.Run("concluded transactions carry their outcome", func(t *testing.T) {
		w := newWallet(t, app)
		now := time.Now()
		cases := map[string]struct {
			overrides  map[string]any
			constraint string
		}{
			"processed without balance": {map[string]any{"balance_after": nil, "balance_currency": nil}, "wt_concluded_has_balance_ck"},
			"rejected without balance": {map[string]any{"status": "REJECTED", "failure_code": "INSUFFICIENT_FUNDS",
				"balance_after": nil, "balance_currency": nil}, "wt_concluded_has_balance_ck"},
			"rejected without code":      {map[string]any{"status": "REJECTED"}, "wt_unsuccessful_has_failure_code_ck"},
			"failed without code":        {map[string]any{"status": "FAILED"}, "wt_unsuccessful_has_failure_code_ck"},
			"terminal without timestamp": {map[string]any{"processed_at": nil}, "wt_terminal_has_processed_at_ck"},
			"balance without currency":   {map[string]any{"balance_currency": nil}, "wt_balance_pair_ck"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				expectViolation(t, insert(app, "wager_transactions", txRow(w, tc.overrides)), pgCheckViolation, tc.constraint)
			})
		}
		failed := txRow(w, map[string]any{"status": "FAILED", "failure_code": "INFRASTRUCTURE_PERMANENT_FAILURE",
			"balance_after": nil, "balance_currency": nil, "processed_at": now})
		mustExec(t, insert(app, "wager_transactions", failed))
	})

	t.Run("terminal transactions are immutable", func(t *testing.T) {
		w := newWallet(t, app)
		tx := newTransaction(t, app, w, map[string]any{"status": "PENDING", "processed_at": nil, "balance_after": nil, "balance_currency": nil})

		_, err := app.Exec(context.Background(),
			`UPDATE wager_transactions SET status = 'REJECTED', failure_code = 'INSUFFICIENT_FUNDS',
			 balance_after = 0, balance_currency = 'BRL', processed_at = now() WHERE id = $1`, tx)
		mustExec(t, err)

		for _, stmt := range []string{
			`UPDATE wager_transactions SET status = 'PROCESSED', failure_code = NULL WHERE id = $1`,
			`UPDATE wager_transactions SET failure_code = 'OTHER' WHERE id = $1`,
			`UPDATE wager_transactions SET attempts = attempts + 1 WHERE id = $1`,
		} {
			_, err := app.Exec(context.Background(), stmt, tx)
			expectCode(t, err, pgIntegrityViolation)
		}
	})

	t.Run("a transaction has at most one successful reversal", func(t *testing.T) {
		w := newWallet(t, app)
		bet := newTransaction(t, app, w, nil)
		reversal := func(kind string) map[string]any {
			return txRow(w, map[string]any{
				"kind": kind, "reference_external_transaction_id": "bet", "reference_transaction_id": bet,
			})
		}
		mustExec(t, insert(app, "wager_transactions", reversal("REFUND")))
		expectViolation(t, insert(app, "wager_transactions", reversal("REFUND")), pgUniqueViolation, "wt_single_successful_reversal_uk")
		expectViolation(t, insert(app, "wager_transactions", reversal("ROLLBACK")), pgUniqueViolation, "wt_single_successful_reversal_uk")

		rejected := reversal("ROLLBACK")
		rejected["status"], rejected["failure_code"] = "REJECTED", "ALREADY_REVERSED"
		mustExec(t, insert(app, "wager_transactions", rejected))
	})

	t.Run("processed reversal requires the resolved reference", func(t *testing.T) {
		w := newWallet(t, app)
		row := txRow(w, map[string]any{"kind": "ROLLBACK", "reference_external_transaction_id": "bet"})
		expectViolation(t, insert(app, "wager_transactions", row), pgCheckViolation, "wt_processed_reversal_has_resolved_reference_ck")
	})

	t.Run("inbox is unique per consumer and message", func(t *testing.T) {
		row := map[string]any{"consumer_name": "wallet-service", "message_id": "msg-1", "payload_hash": "h", "received_at": time.Now()}
		mustExec(t, insert(app, "inbox_messages", row))
		expectViolation(t, insert(app, "inbox_messages", row), pgUniqueViolation, "inbox_messages_pkey")

		row["consumer_name"] = "other-consumer"
		mustExec(t, insert(app, "inbox_messages", row))
	})

	t.Run("outbox snapshot is immutable and publication is final", func(t *testing.T) {
		id := uuid.New()
		mustExec(t, insert(app, "outbox_events", map[string]any{
			"event_id": id, "aggregate_type": "wallet", "aggregate_id": uuid.New(), "event_type": "WalletBalanceChanged",
			"event_version": 1, "payload": `{"eventId":"x"}`, "occurred_at": time.Now(), "next_attempt_at": time.Now(),
		}))

		_, err := app.Exec(context.Background(), `UPDATE outbox_events SET locked_by = 'i-1', locked_until = now(), attempts = 1 WHERE event_id = $1`, id)
		mustExec(t, err)

		for _, stmt := range []string{
			`UPDATE outbox_events SET payload = '{"eventId":"y"}' WHERE event_id = $1`,
			`UPDATE outbox_events SET event_type = 'Other' WHERE event_id = $1`,
		} {
			_, err := app.Exec(context.Background(), stmt, id)
			expectCode(t, err, pgIntegrityViolation)
		}

		_, err = app.Exec(context.Background(), `UPDATE outbox_events SET published_at = now() WHERE event_id = $1`, id)
		mustExec(t, err)
		_, err = app.Exec(context.Background(), `UPDATE outbox_events SET published_at = now() + interval '1 second' WHERE event_id = $1`, id)
		expectCode(t, err, pgIntegrityViolation)
	})
}

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func insert(conn *pgx.Conn, table string, row map[string]any) error {
	columns := slices.Sorted(maps.Keys(row))
	placeholders := make([]string, len(columns))
	args := make([]any, len(columns))
	for i, c := range columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = row[c]
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	_, err := conn.Exec(context.Background(), sql, args...)
	return err
}

func walletRow() map[string]any {
	now := time.Now()
	return map[string]any{
		"id": uuid.New(), "player_id": uuid.New(), "currency": "BRL",
		"balance": 1_000, "version": 1, "created_at": now, "updated_at": now,
	}
}

func newWallet(t *testing.T, conn *pgx.Conn) uuid.UUID {
	t.Helper()
	row := walletRow()
	mustExec(t, insert(conn, "wallets", row))
	return row["id"].(uuid.UUID)
}

func txRow(walletID uuid.UUID, overrides map[string]any) map[string]any {
	now := time.Now()
	ext := uuid.NewString()
	row := map[string]any{
		"id": uuid.New(), "origin": "EXTERNAL", "kind": "BET", "status": "PROCESSED",
		"wallet_id": walletID, "player_id": uuid.New(), "currency": "BRL", "amount": 500,
		"provider_id": "provider-a", "external_transaction_id": ext, "idempotency_key": "provider-a:" + ext,
		"payload_hash": "hash", "round_id": "round-1", "game_id": "game-1",
		"balance_after": 500, "balance_currency": "BRL",
		"created_at": now, "updated_at": now, "processed_at": now,
	}
	for k, v := range overrides {
		row[k] = v
	}
	return row
}

func openingRow(walletID uuid.UUID) map[string]any {
	row := txRow(walletID, map[string]any{"origin": "INTERNAL", "kind": "OPENING"})
	for _, c := range []string{"provider_id", "external_transaction_id", "idempotency_key", "payload_hash", "round_id", "game_id"} {
		delete(row, c)
	}
	return row
}

func newTransaction(t *testing.T, conn *pgx.Conn, walletID uuid.UUID, overrides map[string]any) uuid.UUID {
	t.Helper()
	row := txRow(walletID, overrides)
	mustExec(t, insert(conn, "wager_transactions", row))
	return row["id"].(uuid.UUID)
}

func ledgerRow(walletID, txID uuid.UUID, overrides map[string]any) map[string]any {
	row := map[string]any{
		"id": uuid.New(), "wallet_id": walletID, "transaction_id": txID, "direction": "CREDIT",
		"amount": 500, "currency": "BRL", "balance_before": 1_000, "balance_after": 1_500, "created_at": time.Now(),
	}
	for k, v := range overrides {
		row[k] = v
	}
	return row
}

func mustExec(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	expectViolation(t, err, code, "")
}

func expectViolation(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want postgres error %s %s", err, code, constraint)
	}
	if pgErr.Code != code || (constraint != "" && pgErr.ConstraintName != constraint) {
		t.Fatalf("postgres error %s on %q (%s), want %s on %q", pgErr.Code, pgErr.ConstraintName, pgErr.Message, code, constraint)
	}
}
