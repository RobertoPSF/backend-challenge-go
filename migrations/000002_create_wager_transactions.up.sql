CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    origin                            TEXT        NOT NULL CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    kind                              TEXT        NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status                            TEXT        NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    wallet_id                         UUID        NOT NULL REFERENCES wallets (id),
    player_id                         UUID        NOT NULL,
    currency                          CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount                            BIGINT      NOT NULL CHECK (amount >= 0),
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      TEXT,
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID        REFERENCES wager_transactions (id),
    failure_code                      TEXT,
    balance_after                     BIGINT      CHECK (balance_after >= 0),
    balance_currency                  CHAR(3)     CHECK (balance_currency ~ '^[A-Z]{3}$'),
    attempts                          INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at                   TIMESTAMPTZ,
    expires_at                        TIMESTAMPTZ,
    correlation_id                    TEXT,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,
    processed_at                      TIMESTAMPTZ,

    CONSTRAINT wt_id_wallet_uk UNIQUE (id, wallet_id),

    CONSTRAINT wt_origin_matches_kind_ck CHECK ((origin = 'INTERNAL') = (kind = 'OPENING')),
    CONSTRAINT wt_internal_has_no_external_data_ck CHECK (
        origin <> 'INTERNAL' OR (
            provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL)),
    CONSTRAINT wt_external_has_required_data_ck CHECK (
        origin <> 'EXTERNAL' OR (
            COALESCE(provider_id, '') <> '' AND COALESCE(external_transaction_id, '') <> ''
            AND COALESCE(idempotency_key, '') <> '' AND COALESCE(payload_hash, '') <> ''
            AND COALESCE(round_id, '') <> '' AND COALESCE(game_id, '') <> '')),
    CONSTRAINT wt_reversal_has_reference_ck CHECK (
        kind NOT IN ('REFUND', 'ROLLBACK') OR COALESCE(reference_external_transaction_id, '') <> ''),
    CONSTRAINT wt_bet_loss_have_no_reference_ck CHECK (
        kind NOT IN ('BET', 'LOSS') OR reference_external_transaction_id IS NULL),
    CONSTRAINT wt_amount_by_kind_ck CHECK ((kind = 'LOSS') = (amount = 0)),

    CONSTRAINT wt_balance_pair_ck CHECK ((balance_after IS NULL) = (balance_currency IS NULL)),
    CONSTRAINT wt_terminal_has_processed_at_ck CHECK (
        status NOT IN ('PROCESSED', 'REJECTED', 'FAILED') OR processed_at IS NOT NULL),
    CONSTRAINT wt_concluded_has_balance_ck CHECK (
        status NOT IN ('PROCESSED', 'REJECTED') OR balance_after IS NOT NULL),
    CONSTRAINT wt_unsuccessful_has_failure_code_ck CHECK (
        status NOT IN ('REJECTED', 'FAILED') OR failure_code IS NOT NULL),
    CONSTRAINT wt_processed_reversal_has_resolved_reference_ck CHECK (
        status <> 'PROCESSED' OR kind NOT IN ('REFUND', 'ROLLBACK') OR reference_transaction_id IS NOT NULL)
);

CREATE UNIQUE INDEX wt_provider_idempotency_key_uk
    ON wager_transactions (provider_id, idempotency_key) WHERE origin = 'EXTERNAL';

CREATE UNIQUE INDEX wt_provider_external_id_uk
    ON wager_transactions (provider_id, external_transaction_id) WHERE origin = 'EXTERNAL';

CREATE UNIQUE INDEX wt_single_opening_per_wallet_uk
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wt_single_successful_reversal_uk
    ON wager_transactions (reference_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

CREATE INDEX wt_due_pending_idx
    ON wager_transactions (next_attempt_at) WHERE status IN ('PENDING', 'PENDING_REFERENCE');

CREATE INDEX wt_waiting_reference_idx
    ON wager_transactions (provider_id, reference_external_transaction_id) WHERE status = 'PENDING_REFERENCE';

CREATE FUNCTION wager_transactions_forbid_terminal_update() RETURNS trigger AS $$
BEGIN
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager transaction % is terminal (%) and cannot be changed', OLD.id, OLD.status
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wager_transactions_terminal_guard
    BEFORE UPDATE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_forbid_terminal_update();

GRANT SELECT, INSERT, UPDATE ON wager_transactions TO wallet_app;
