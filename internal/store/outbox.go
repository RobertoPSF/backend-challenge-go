package store

import (
	"context"
	"encoding/json"
	"fmt"

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
