package refresh

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

type GCWorkerConfig struct {
	Store     Store
	Clock     clock.Clock
	Interval  time.Duration
	Retention time.Duration
	Logger    *slog.Logger
	Metrics   GCCollector
}

type GCWorker struct {
	store     Store
	clock     clock.Clock
	interval  time.Duration
	retention time.Duration
	logger    *slog.Logger
	metrics   GCCollector

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewGCWorker(cfg GCWorkerConfig) (*GCWorker, error) {
	if cfg.Store == nil {
		return nil, errcode.New(errcode.ErrCellInvalidConfig, "refresh gc: store is required")
	}
	if cfg.Clock == nil {
		return nil, errcode.New(errcode.ErrCellInvalidConfig, "refresh gc: clock is required")
	}
	if cfg.Interval <= 0 {
		return nil, errcode.New(errcode.ErrCellInvalidConfig, "refresh gc: interval must be positive")
	}
	if cfg.Retention <= 0 {
		return nil, errcode.New(errcode.ErrCellInvalidConfig, "refresh gc: retention must be positive")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NoopGCCollector{}
	}
	return &GCWorker{
		store:     cfg.Store,
		clock:     cfg.Clock,
		interval:  cfg.Interval,
		retention: cfg.Retention,
		logger:    cfg.Logger,
		metrics:   cfg.Metrics,
	}, nil
}

func (w *GCWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.loop(runCtx, w.done)
	return nil
}

func (w *GCWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel := w.cancel
	done := w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		w.mu.Lock()
		w.cancel = nil
		w.done = nil
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *GCWorker) loop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	w.runOnce(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *GCWorker) runOnce(ctx context.Context) {
	start := w.clock.Now()
	olderThan := start.Add(-w.retention)
	removed, err := w.store.GC(ctx, olderThan)
	duration := w.clock.Since(start)
	if err != nil {
		w.logger.Error("refresh gc failed", slog.Any("error", err))
		w.metrics.ObserveRefreshGC(ctx, "failure", 0, duration)
		return
	}
	w.logger.Info("refresh gc completed", slog.Int("removed", removed), slog.Duration("duration", duration))
	w.metrics.ObserveRefreshGC(ctx, "success", removed, duration)
}
