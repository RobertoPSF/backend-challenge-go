package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
)

const walletAggregate = "wallet"

type OutboxRepo struct {
	q querier
}

func (r OutboxRepo) Insert(ctx context.Context, events ...domain.Event) error {
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", event.Header().EventType, err)
		}
		h := event.Header()
		_, err = r.q.Exec(ctx,
			`INSERT INTO outbox_events (event_id, aggregate_type, aggregate_id, event_type, event_version,
				payload, correlation_id, occurred_at, next_attempt_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
			h.EventID, walletAggregate, h.AggregateID, h.EventType, h.Version, payload, nullIfEmpty(h.CorrelationID), h.OccurredAt)
		if err != nil {
			return err
		}
	}
	return nil
}

type OutboxEvent struct {
	EventID     uuid.UUID
	AggregateID uuid.UUID
	EventType   string
	Version     int
	Payload     []byte
	Attempts    int
}

func (r OutboxRepo) ClaimBatch(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]OutboxEvent, error) {
	rows, err := r.q.Query(ctx, `
		WITH heads AS (
			SELECT DISTINCT ON (aggregate_id) event_id
			FROM outbox_events
			WHERE published_at IS NULL
			ORDER BY aggregate_id, occurred_at, event_id
		)
		UPDATE outbox_events o
		SET locked_by = $1, locked_until = $3, attempts = o.attempts + 1
		WHERE o.event_id IN (
			SELECT e.event_id FROM outbox_events e JOIN heads h ON h.event_id = e.event_id
			WHERE e.next_attempt_at <= $2 AND (e.locked_until IS NULL OR e.locked_until < $2)
			ORDER BY e.occurred_at
			LIMIT $4
			FOR UPDATE OF e SKIP LOCKED)
		RETURNING o.event_id, o.aggregate_id, o.event_type, o.event_version, o.payload, o.attempts`,
		owner, now, now.Add(lease), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (OutboxEvent, error) {
		var e OutboxEvent
		err := row.Scan(&e.EventID, &e.AggregateID, &e.EventType, &e.Version, &e.Payload, &e.Attempts)
		return e, err
	})
}

func (r OutboxRepo) MarkPublished(ctx context.Context, eventID uuid.UUID, owner string, now time.Time) error {
	_, err := r.q.Exec(ctx, `UPDATE outbox_events
		SET published_at = $1, locked_by = NULL, locked_until = NULL, last_error = NULL
		WHERE event_id = $2 AND locked_by = $3 AND published_at IS NULL`, now, eventID, owner)
	return err
}

func (r OutboxRepo) MarkFailed(ctx context.Context, eventID uuid.UUID, owner string, nextAttemptAt time.Time, cause string) error {
	_, err := r.q.Exec(ctx, `UPDATE outbox_events
		SET next_attempt_at = $1, last_error = $2, locked_by = NULL, locked_until = NULL
		WHERE event_id = $3 AND locked_by = $4 AND published_at IS NULL`, nextAttemptAt, cause, eventID, owner)
	return err
}

func (r OutboxRepo) Release(ctx context.Context, eventIDs []uuid.UUID, owner string) error {
	_, err := r.q.Exec(ctx, `UPDATE outbox_events SET locked_by = NULL, locked_until = NULL
		WHERE event_id = ANY($1) AND locked_by = $2 AND published_at IS NULL`, eventIDs, owner)
	return err
}
