CREATE INDEX outbox_unpublished_by_aggregate_idx
    ON outbox_events (aggregate_id, occurred_at, event_id) WHERE published_at IS NULL;
