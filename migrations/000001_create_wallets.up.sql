CREATE TABLE wallets (
    id         UUID        PRIMARY KEY,
    player_id  UUID        NOT NULL,
    currency   CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance    BIGINT      NOT NULL CHECK (balance >= 0),
    version    BIGINT      NOT NULL CHECK (version >= 1),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_player_currency_uk UNIQUE (player_id, currency)
);

GRANT SELECT, INSERT, UPDATE ON wallets TO wallet_app;
