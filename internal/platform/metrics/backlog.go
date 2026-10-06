package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Backlog struct {
	PendingEvents        int64
	OldestPendingAge     time.Duration
	WaitingForReferences int64
}

type BacklogFunc func(ctx context.Context) (Backlog, error)

type backlogCollector struct {
	read    BacklogFunc
	log     *slog.Logger
	pending *prometheus.Desc
	age     *prometheus.Desc
	waiting *prometheus.Desc
}

func NewBacklogCollector(read BacklogFunc, log *slog.Logger) prometheus.Collector {
	return &backlogCollector{
		read: read,
		log:  log,
		pending: prometheus.NewDesc("outbox_pending_events",
			"Outbox events committed but not yet published, read from the database at scrape time.", nil, nil),
		age: prometheus.NewDesc("outbox_oldest_pending_age_seconds",
			"Age of the oldest unpublished outbox event (publication lag); 0 when nothing is pending.", nil, nil),
		waiting: prometheus.NewDesc("pending_references_waiting",
			"Operations in PENDING_REFERENCE waiting for the referenced transaction.", nil, nil),
	}
}

func (c *backlogCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.pending
	ch <- c.age
	ch <- c.waiting
}

func (c *backlogCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := c.read(ctx)
	if err != nil {
		c.log.Warn("could not read backlog metrics", "error", err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(b.PendingEvents))
	ch <- prometheus.MustNewConstMetric(c.age, prometheus.GaugeValue, b.OldestPendingAge.Seconds())
	ch <- prometheus.MustNewConstMetric(c.waiting, prometheus.GaugeValue, float64(b.WaitingForReferences))
}
