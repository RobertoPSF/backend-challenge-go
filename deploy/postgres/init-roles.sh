#!/bin/sh

set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
CREATE ROLE wallet_owner LOGIN PASSWORD '${WALLET_OWNER_PASSWORD}';
CREATE ROLE wallet_app   LOGIN PASSWORD '${WALLET_APP_PASSWORD}';

-- Dono do banco => dono do schema public (pg_database_owner, Postgres 15+).
ALTER DATABASE "$POSTGRES_DB" OWNER TO wallet_owner;

REVOKE ALL ON DATABASE "$POSTGRES_DB" FROM PUBLIC;
GRANT CONNECT ON DATABASE "$POSTGRES_DB" TO wallet_app;
GRANT USAGE ON SCHEMA public TO wallet_app;
SQL
