package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Loop is one unit of background work. RunOnce receives a context that is
// cancelled when shutdown starts: it must stop fetching new work, and use
// context.WithoutCancel for anything that has to finish or be released.
type Loop interface {
	Name() string
	RunOnce(ctx context.Context) (didWork bool, err error)
}

type Runner struct {
	loop        Loop
	concurrency int
	idleDelay   time.Duration
	log         *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewRunner(loop Loop, concurrency int, idleDelay time.Duration, log *slog.Logger) *Runner {
	return &Runner{
		loop:        loop,
		concurrency: max(concurrency, 1),
		idleDelay:   idleDelay,
		log:         log.With("worker", loop.Name()),
	}
}

func (r *Runner) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	for range r.concurrency {
		r.wg.Go(func() { r.run(ctx) })
	}
	r.log.Info("worker started", "concurrency", r.concurrency)
	return nil
}

func (r *Runner) Stop(ctx context.Context) error {
	r.cancel()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		r.log.Info("worker stopped")
		return nil
	case <-ctx.Done():
		r.log.Error("worker did not stop before the shutdown deadline")
		return fmt.Errorf("worker %s: %w", r.loop.Name(), ctx.Err())
	}
}

func (r *Runner) run(ctx context.Context) {
	for ctx.Err() == nil {
		didWork, err := r.loop.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.log.Error("worker iteration failed", "error", err)
		}
		if err != nil || !didWork {
			sleep(ctx, r.idleDelay)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
