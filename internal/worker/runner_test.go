package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

type fakeLoop struct {
	calls   atomic.Int64
	runOnce func(ctx context.Context) (bool, error)
}

func (f *fakeLoop) Name() string { return "fake" }

func (f *fakeLoop) RunOnce(ctx context.Context) (bool, error) {
	f.calls.Add(1)
	return f.runOnce(ctx)
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRunner_RunsConcurrentlyAndStopsCleanly(t *testing.T) {
	defer goleak.VerifyNone(t)

	var inFlight, maxInFlight atomic.Int64
	loop := &fakeLoop{runOnce: func(context.Context) (bool, error) {
		n := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return true, nil
	}}

	r := NewRunner(loop, 3, time.Millisecond, discard)
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop() = %v, want nil", err)
	}
	if maxInFlight.Load() != 3 {
		t.Errorf("max concurrent iterations = %d, want 3", maxInFlight.Load())
	}
}

func TestRunner_BacksOffWhenIdleOrFailing(t *testing.T) {
	defer goleak.VerifyNone(t)

	for name, result := range map[string]error{"idle": nil, "failing": errors.New("boom")} {
		t.Run(name, func(t *testing.T) {
			loop := &fakeLoop{runOnce: func(context.Context) (bool, error) { return false, result }}
			r := NewRunner(loop, 1, 20*time.Millisecond, discard)
			_ = r.Start(context.Background())
			time.Sleep(70 * time.Millisecond)
			_ = r.Stop(context.Background())

			if calls := loop.calls.Load(); calls < 2 || calls > 5 {
				t.Errorf("calls = %d, want between 2 and 5 with a 20ms idle delay over 70ms", calls)
			}
		})
	}
}

func TestRunner_CancelsIterationContextOnStop(t *testing.T) {
	defer goleak.VerifyNone(t)

	started, cancelled := make(chan struct{}), make(chan struct{})
	loop := &fakeLoop{runOnce: func(ctx context.Context) (bool, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return false, ctx.Err()
	}}
	r := NewRunner(loop, 1, time.Millisecond, discard)
	_ = r.Start(context.Background())
	<-started

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v, want nil", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("iteration context was not cancelled on Stop")
	}
}

func TestRunner_StopReturnsErrorWhenDeadlineExpires(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	loop := &fakeLoop{runOnce: func(context.Context) (bool, error) {
		close(started)
		<-release
		return true, nil
	}}
	r := NewRunner(loop, 1, time.Millisecond, discard)
	_ = r.Start(context.Background())
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := r.Stop(ctx)

	close(release)
	r.wg.Wait()

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() = %v, want context.DeadlineExceeded", err)
	}
}
