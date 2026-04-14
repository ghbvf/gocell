package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Compile-time interface check.
var _ Worker = (*PeriodicWorker)(nil)

// PeriodicWorker runs a function at a fixed interval. Panics in the function
// are isolated and logged, not propagated.
type PeriodicWorker struct {
	mu       sync.Mutex
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
// Each call to Start creates a fresh done channel, so a PeriodicWorker can be
// restarted after Stop.
func (p *PeriodicWorker) Start(ctx context.Context) error {
	p.mu.Lock()
	p.done = make(chan struct{})
	done := p.done
	p.mu.Unlock()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		case <-ticker.C:
			p.runSafe(ctx)
		}
	}
}

// Stop signals the worker to exit.
func (p *PeriodicWorker) Stop(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
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
