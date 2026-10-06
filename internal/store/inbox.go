package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type InboxRepo struct {
	q querier
}

func (r InboxRepo) Insert(ctx context.Context, consumer, messageID, hash string, now time.Time) (bool, error) {
	tag, err := r.q.Exec(ctx, `INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, consumer, messageID, hash, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r InboxRepo) Hash(ctx context.Context, consumer, messageID string) (string, error) {
	var hash string
	err := r.q.QueryRow(ctx, `SELECT payload_hash FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`,
		consumer, messageID).Scan(&hash)
	return hash, err
}

func (r InboxRepo) Complete(ctx context.Context, consumer, messageID string, transactionID uuid.UUID, now time.Time) error {
	_, err := r.q.Exec(ctx, `UPDATE inbox_messages SET transaction_id = $1, completed_at = $2
		WHERE consumer_name = $3 AND message_id = $4`, transactionID, now, consumer, messageID)
	return err
}
