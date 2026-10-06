CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL CHECK (consumer_name <> ''),
    message_id     TEXT        NOT NULL CHECK (message_id <> ''),
    payload_hash   TEXT        NOT NULL CHECK (payload_hash <> ''),
    transaction_id UUID        REFERENCES wager_transactions (id),
    received_at    TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ,

    PRIMARY KEY (consumer_name, message_id)
);

GRANT SELECT, INSERT, UPDATE ON inbox_messages TO wallet_app;
