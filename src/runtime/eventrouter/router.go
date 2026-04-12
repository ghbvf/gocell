// Package eventrouter provides a Router that separates event subscription
// declaration from execution. Cells declare handlers via AddHandler; the
// Router owns goroutine lifecycle, setup-error detection, and graceful shutdown.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddHandler/Run/Close pattern.
// Adopted: declaration-then-run split, Running() readiness signal, WaitGroup goroutine tracking.
// Deviated: no publish-side in AddHandler (GoCell publishes via outbox.Writer, not Router);
// startup detection uses a configurable timeout because outbox.Subscriber.Subscribe blocks
// without a separate setup/run split.
package eventrouter

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// DefaultStartupTimeout is the duration Run waits for Subscribe calls to
// either return an error (setup failure) or remain blocking (consuming).
// If no error arrives within this window, all subscriptions are assumed ready.
//
// Note: this is a heuristic. If broker topology setup (e.g., RabbitMQ
// ExchangeDeclare + QueueBind) takes longer than this timeout, bootstrap
// will proceed before subscriptions are actually ready. The timeout is
// configurable via WithStartupTimeout. A future Subscriber interface split
// (Setup + Run) would eliminate this heuristic entirely.
const DefaultStartupTimeout = 500 * time.Millisecond

// Option configures a Router.
type Option func(*Router)

// WithStartupTimeout overrides the default startup detection timeout.
func WithStartupTimeout(d time.Duration) Option {
	return func(r *Router) {
		if d > 0 {
			r.startupTimeout = d
		}
	}
}

type handlerConfig struct {
	topic         string
	handler       outbox.EntryHandler
	consumerGroup string
}

// Router manages event subscription lifecycle. It implements cell.EventRouter
// for the declaration phase (AddHandler) and provides Run/Close for the
// execution phase.
//
// Run MUST be called at most once. Calling Run a second time returns an error.
type Router struct {
	subscriber     outbox.Subscriber
	handlers       []handlerConfig
	mu             sync.Mutex
	startupTimeout time.Duration
	running        chan struct{}
	runGuard       sync.Once // ensures Run is called at most once
	runningOnce    sync.Once // ensures close(r.running) is called at most once
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

// Compile-time interface check.
var _ cell.EventRouter = (*Router)(nil)

// New creates a Router that will use the given Subscriber for all subscriptions.
func New(sub outbox.Subscriber, opts ...Option) *Router {
	r := &Router{
		subscriber:     sub,
		startupTimeout: DefaultStartupTimeout,
		running:        make(chan struct{}),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// AddHandler registers a subscription intent. It MUST be called before Run.
// Panics if topic is empty or handler is nil.
//
// consumerGroup identifies the logical consumer group for this handler.
// Handlers in the same group compete for messages on the same topic;
// different groups each receive a full copy (fanout). Cell implementations
// typically pass their cell ID (e.g. "audit-core") to ensure per-cell isolation.
func (r *Router) AddHandler(topic string, handler outbox.EntryHandler, consumerGroup string) {
	if topic == "" {
		panic("eventrouter: AddHandler called with empty topic")
	}
	if handler == nil {
		panic("eventrouter: AddHandler called with nil handler")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers = append(r.handlers, handlerConfig{topic: topic, handler: handler, consumerGroup: consumerGroup})
}

// errAlreadyRunning is returned if Run is called more than once.
var errAlreadyRunning = fmt.Errorf("eventrouter: Run called more than once")

// Run starts all registered subscriptions and blocks until ctx is cancelled
// or an unrecoverable subscription error occurs.
//
// Run MUST be called at most once; a second call returns errAlreadyRunning.
//
// Setup-error detection: each Subscribe call is launched in a goroutine.
// If any returns an error within the startup timeout, Run cancels all
// goroutines and returns the error. If no error arrives within the timeout,
// all subscriptions are assumed to be consuming and Running() is closed.
//
// On context cancellation, Run waits for all goroutines to finish before
// returning.
func (r *Router) Run(ctx context.Context) error {
	var firstRun bool
	r.runGuard.Do(func() { firstRun = true })
	if !firstRun {
		return errAlreadyRunning
	}

	r.mu.Lock()
	handlers := make([]handlerConfig, len(r.handlers))
	copy(handlers, r.handlers)
	r.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	if len(handlers) == 0 {
		r.closeRunning()
		<-runCtx.Done()
		return nil
	}

	// setupErr receives the first subscription setup error.
	// Buffer size = len(handlers) to avoid goroutine leaks if multiple fail.
	setupErr := make(chan error, len(handlers))

	for _, h := range handlers {
		h := h
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer func() {
				if rv := recover(); rv != nil {
					setupErr <- fmt.Errorf("eventrouter: topic %s panicked: %v", h.topic, rv)
				}
			}()
			slog.Info("eventrouter: starting subscription",
				slog.String("topic", h.topic))
			err := r.subscriber.Subscribe(runCtx, h.topic, h.handler, h.consumerGroup)
			if err != nil && runCtx.Err() == nil {
				setupErr <- fmt.Errorf("eventrouter: topic %s: %w", h.topic, err)
			}
		}()
	}

	// Wait for setup phase: either an error arrives or startup timeout passes.
	select {
	case err := <-setupErr:
		slog.Error("eventrouter: subscription setup failed, shutting down",
			slog.Any("error", err))
		cancel()
		r.wg.Wait()
		return err
	case <-time.After(r.startupTimeout):
		// No errors within timeout — all handlers are consuming.
		slog.Info("eventrouter: all subscriptions started",
			slog.Int("count", len(handlers)))
		r.closeRunning()
	case <-runCtx.Done():
		// Context cancelled during startup.
		r.wg.Wait()
		return runCtx.Err()
	}

	// Block until context cancelled or a runtime error surfaces.
	select {
	case <-runCtx.Done():
	case err := <-setupErr:
		slog.Error("eventrouter: subscription failed at runtime",
			slog.Any("error", err))
		cancel()
		r.wg.Wait()
		return err
	}

	r.wg.Wait()
	return nil
}

// closeRunning safely closes the running channel exactly once.
func (r *Router) closeRunning() {
	r.runningOnce.Do(func() { close(r.running) })
}

// Running returns a channel that is closed when all subscriptions have
// successfully started consuming. Callers can use this to wait for the
// Router to be ready (e.g., in bootstrap).
//
// Note: if Run returns a setup error, Running() is never closed. Callers
// should also monitor the error from Run.
func (r *Router) Running() <-chan struct{} {
	return r.running
}

// Close cancels all subscriptions and waits for goroutines to finish.
// The provided context controls the maximum wait time for goroutines to
// drain; if the context expires, Close returns the context error.
func (r *Router) Close(ctx context.Context) error {
	start := time.Now()

	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("eventrouter: closed", slog.Duration("elapsed", time.Since(start)))
		return nil
	case <-ctx.Done():
		slog.Warn("eventrouter: close timed out, some goroutines may still be running",
			slog.Duration("elapsed", time.Since(start)))
		return ctx.Err()
	}
}

// HandlerCount returns the number of registered handlers.
func (r *Router) HandlerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.handlers)
}
