CREATE TABLE wallet_ledger_entries (
    id             UUID        PRIMARY KEY,
    wallet_id      UUID        NOT NULL REFERENCES wallets (id),
    transaction_id UUID        NOT NULL,
    direction      TEXT        NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount         BIGINT      NOT NULL CHECK (amount > 0),
    currency       CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_before BIGINT      NOT NULL CHECK (balance_before >= 0),
    balance_after  BIGINT      NOT NULL CHECK (balance_after >= 0),
    created_at     TIMESTAMPTZ NOT NULL,

    CONSTRAINT ledger_wallet_transaction_uk UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_transaction_belongs_to_wallet_fk
        FOREIGN KEY (transaction_id, wallet_id) REFERENCES wager_transactions (id, wallet_id),
    CONSTRAINT ledger_balance_math_ck CHECK (
        (direction = 'CREDIT' AND balance_after = balance_before + amount) OR
        (direction = 'DEBIT' AND balance_after = balance_before - amount))
);

CREATE INDEX ledger_wallet_cursor_idx ON wallet_ledger_entries (wallet_id, created_at, id);

CREATE FUNCTION wallet_ledger_entries_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallet_ledger_entries_no_update_delete
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_entries_forbid_mutation();

CREATE TRIGGER wallet_ledger_entries_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION wallet_ledger_entries_forbid_mutation();

GRANT SELECT, INSERT ON wallet_ledger_entries TO wallet_app;
