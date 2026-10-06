CREATE TABLE outbox_events (
    event_id        UUID        PRIMARY KEY,
    aggregate_type  TEXT        NOT NULL CHECK (aggregate_type <> ''),
    aggregate_id    UUID        NOT NULL,
    event_type      TEXT        NOT NULL CHECK (event_type <> ''),
    event_version   INT         NOT NULL CHECK (event_version >= 1),
    payload         JSONB       NOT NULL,
    correlation_id  TEXT,
    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_by       TEXT,
    locked_until    TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    last_error      TEXT
);

CREATE INDEX outbox_unpublished_idx ON outbox_events (next_attempt_at) WHERE published_at IS NULL;

CREATE FUNCTION outbox_events_forbid_snapshot_change() RETURNS trigger AS $$
BEGIN
    IF NEW.event_id IS DISTINCT FROM OLD.event_id
        OR NEW.aggregate_type IS DISTINCT FROM OLD.aggregate_type
        OR NEW.aggregate_id IS DISTINCT FROM OLD.aggregate_id
        OR NEW.event_type IS DISTINCT FROM OLD.event_type
        OR NEW.event_version IS DISTINCT FROM OLD.event_version
        OR NEW.payload IS DISTINCT FROM OLD.payload
        OR NEW.correlation_id IS DISTINCT FROM OLD.correlation_id
        OR NEW.occurred_at IS DISTINCT FROM OLD.occurred_at THEN
        RAISE EXCEPTION 'outbox event % snapshot is immutable', OLD.event_id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN
        RAISE EXCEPTION 'outbox event % is already published', OLD.event_id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER outbox_events_snapshot_guard
    BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_events_forbid_snapshot_change();

GRANT SELECT, INSERT, UPDATE ON outbox_events TO wallet_app;
