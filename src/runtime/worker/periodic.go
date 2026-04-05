package worker

import (
	"context"
	"log/slog"
	"time"
)

// PeriodicWorker runs a function at a fixed interval. Panics in the function
// are isolated and logged, not propagated.
type PeriodicWorker struct {
	interval time.Duration
	fn       func(ctx context.Context)
	done     chan struct{}
}

// NewPeriodicWorker creates a PeriodicWorker that runs fn every interval.
func NewPeriodicWorker(interval time.Duration, fn func(ctx context.Context)) *PeriodicWorker {
	return &PeriodicWorker{
		interval: interval,
		fn:       fn,
		done:     make(chan struct{}),
	}
}

// Start runs the periodic function until ctx is cancelled or Stop is called.
func (p *PeriodicWorker) Start(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return nil
		case <-ticker.C:
			p.runSafe(ctx)
		}
	}
}

// Stop signals the worker to exit.
func (p *PeriodicWorker) Stop(_ context.Context) error {
	select {
	case <-p.done:
		// Already stopped.
	default:
		close(p.done)
	}
	return nil
}

func (p *PeriodicWorker) runSafe(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("periodic worker panic", slog.Any("panic", r))
		}
	}()
	p.fn(ctx)
}
