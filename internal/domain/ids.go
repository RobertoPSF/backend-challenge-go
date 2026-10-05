package domain

import (
	"time"

	"github.com/google/uuid"
)

func newID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

func normalizeTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}
