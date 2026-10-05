package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
)

const readinessCheckTimeout = 2 * time.Second

type Health struct {
	pool *pgxpool.Pool
	sqs  *sqsclient.Client
	log  *slog.Logger
}

func NewHealth(pool *pgxpool.Pool, sqsClient *sqsclient.Client, log *slog.Logger) *Health {
	return &Health{pool: pool, sqs: sqsClient, log: log}
}

func (h *Health) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	checks := map[string]func(context.Context) error{
		"postgres": h.pool.Ping,
		"sqs": func(ctx context.Context) error {
			_, err := h.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
				QueueUrl:       &h.sqs.Queues.InputURL,
				AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
			})
			return err
		},
	}

	status := http.StatusOK
	results := make(map[string]string, len(checks))
	for name, check := range checks {
		ctx, cancel := context.WithTimeout(r.Context(), readinessCheckTimeout)
		err := check(ctx)
		cancel()
		if err != nil {
			h.log.Warn("readiness check failed", "dependency", name, "error", err)
			results[name] = "unavailable"
			status = http.StatusServiceUnavailable
			continue
		}
		results[name] = "ok"
	}

	overall := "ok"
	if status != http.StatusOK {
		overall = "unavailable"
	}
	writeJSON(w, status, map[string]any{"status": overall, "checks": results})
}
