// Package eventrouter provides a Router that separates event subscription
// declaration from execution. Cells declare handlers via AddHandler; the
// Router owns goroutine lifecycle, setup-error detection, and graceful shutdown.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddHandler/Run/Close pattern.
// Adopted: declaration-then-run split, Running() readiness signal, WaitGroup goroutine tracking.
// Deviated: no publish-side in AddHandler (GoCell publishes via outbox.Writer, not Router);
// startup readiness uses an explicit Ready signal from the Subscriber interface rather
// than a timeout heuristic — aligned with Uber fx OnStart synchronous return semantics.
//
// Run lifecycle (4 phases):
//
//	Phase 1: serially call Subscriber.Setup(sub) for each handler — blocking topology declaration.
//	Phase 2: launch one Subscribe goroutine per handler concurrently.
//	Phase 3: wait on Subscriber.Ready(sub) for every handler (explicit signal, no timeout).
//	Phase 4: block until ctx cancel or a runtime subscription error.
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

// DefaultReadyTimeout bounds Phase 3 of Run() so a subscriber that never
// signals Ready (broker reconnect storm, mis-configured topology) does not
// block bootstrap indefinitely. 30s aligns with Uber fx StartTimeout default.
// Set to a non-positive value via WithReadyTimeout to disable the bound.
const DefaultReadyTimeout = 30 * time.Second

// Option configures a Router.
type Option func(*Router)

// WithReadyTimeout overrides the default ready-wait budget. A value <= 0
// disables the timeout (waits indefinitely on Ready channels and ctx).
func WithReadyTimeout(d time.Duration) Option {
	return func(r *Router) { r.readyTimeout = d }
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
	subscriber   outbox.Subscriber
	handlers     []handlerConfig
	mu           sync.Mutex
	readyTimeout time.Duration
	running      chan struct{}
	runGuard     sync.Once // ensures Run is called at most once
	runningOnce  sync.Once // ensures close(r.running) is called at most once
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	statusMu     sync.RWMutex
	started      bool
	shutdown     bool
	healthErr    error
}

// Compile-time interface check.
var _ cell.EventRouter = (*Router)(nil)

// New creates a Router that will use the given Subscriber for all subscriptions.
func New(sub outbox.Subscriber, opts ...Option) *Router {
	r := &Router{
		subscriber:   sub,
		readyTimeout: DefaultReadyTimeout,
		running:      make(chan struct{}),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// AddHandler registers a subscription intent. It MUST be called before Run.
// Panics if topic is empty, handler is nil, or consumerGroup is empty.
//
// consumerGroup identifies the logical consumer group for this handler.
// Handlers in the same group compete for messages on the same topic;
// different groups each receive a full copy (fanout). Cell implementations
// MUST pass their cell ID (e.g. "audit-core") to ensure per-cell isolation
// and portable semantics across all backends. Empty consumerGroup is
// rejected to prevent silent backend-specific behavior divergence.
func (r *Router) AddHandler(topic string, handler outbox.EntryHandler, consumerGroup string) {
	if topic == "" {
		panic("eventrouter: AddHandler called with empty topic")
	}
	if handler == nil {
		panic("eventrouter: AddHandler called with nil handler")
	}
	if consumerGroup == "" {
		panic("eventrouter: AddHandler called with empty consumerGroup; cells must declare their identity")
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
// Lifecycle (4 phases):
//
//	Phase 1: serially call Subscriber.Setup(sub) — blocking topology declaration.
//	         Any Setup error causes Run to return immediately without starting subscriptions.
//	Phase 2: launch one Subscribe goroutine per handler concurrently.
//	Phase 3: wait on Subscriber.Ready(sub) for every handler.
//	         Running() is closed only after ALL Ready channels close.
//	Phase 4: block until ctx cancel or a runtime subscription error.
//
// On context cancellation, Run waits for all goroutines to finish before returning.
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
		r.markRunning()
		r.closeRunning()
		<-runCtx.Done()
		cancel()
		return nil
	}

	// Phase 1: serially Setup each subscription (topology declaration).
	if err := r.runSetup(runCtx, cancel, handlers); err != nil {
		return err
	}

	// setupErr receives the first subscription runtime error.
	// Buffer size = len(handlers) to avoid goroutine leaks if multiple fail.
	setupErr := make(chan error, len(handlers))

	// Phase 2: launch Subscribe goroutines concurrently.
	r.runSubscribe(runCtx, handlers, setupErr)

	// Phase 3: wait for all Ready signals before marking Running.
	if err := r.runAwaitReady(runCtx, cancel, handlers, setupErr); err != nil {
		return err
	}

	r.markRunning()
	slog.Info("eventrouter: all subscriptions ready (explicit Ready signal)",
		slog.Int("count", len(handlers)))
	r.closeRunning()

	// Phase 4: block until context cancelled or a runtime error surfaces.
	select {
	case <-runCtx.Done():
		r.markShutdown()
	case err := <-setupErr:
		r.markHealthError(err)
		slog.Error("eventrouter: subscription failed at runtime",
			slog.Any("error", err))
		cancel()
		r.wg.Wait()
		return err
	}

	r.wg.Wait()
	return nil
}

// runSetup calls Subscriber.Setup for each handler sequentially (Phase 1).
// On error, it cancels the context and returns the wrapped error.
func (r *Router) runSetup(ctx context.Context, cancel context.CancelFunc, handlers []handlerConfig) error {
	for _, h := range handlers {
		sub := outbox.Subscription{
			Topic:         h.topic,
			ConsumerGroup: h.consumerGroup,
			CellID:        h.consumerGroup,
		}
		if err := r.subscriber.Setup(ctx, sub); err != nil {
			wrapped := fmt.Errorf("eventrouter: setup %s: %w", sub.Topic, err)
			r.markHealthError(wrapped)
			slog.Error("eventrouter: subscription setup failed, aborting",
				slog.String("topic", sub.Topic),
				slog.Any("error", err))
			cancel()
			return wrapped
		}
	}
	return nil
}

// runSubscribe starts one goroutine per handler that calls Subscriber.Subscribe
// (Phase 2). Errors are sent to setupErr.
func (r *Router) runSubscribe(ctx context.Context, handlers []handlerConfig, setupErr chan<- error) {
	for _, h := range handlers {
		h := h
		sub := outbox.Subscription{
			Topic:         h.topic,
			ConsumerGroup: h.consumerGroup,
			CellID:        h.consumerGroup,
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer func() {
				if rv := recover(); rv != nil {
					setupErr <- fmt.Errorf("eventrouter: topic %s panicked: %v", sub.Topic, rv)
				}
			}()
			slog.Info("eventrouter: starting subscription",
				slog.String("topic", sub.Topic),
				slog.String("consumer_group", sub.ConsumerGroup))
			err := r.subscriber.Subscribe(ctx, sub, h.handler)
			if err != nil && ctx.Err() == nil {
				setupErr <- fmt.Errorf("eventrouter: topic %s: %w", sub.Topic, err)
			}
		}()
	}
}

// runAwaitReady waits for Subscriber.Ready(sub) for ALL handlers concurrently
// (Phase 3). Concurrent fan-out avoids goroutine-schedule coupling where a
// serial wait on handler[0] would block if handler[1]'s goroutine happened to
// register first with the in-memory bus.
//
// It also monitors setupErr and ctx cancellation. On error, it cancels the
// context, waits for goroutines, and returns the error.
func (r *Router) runAwaitReady(ctx context.Context, cancel context.CancelFunc, handlers []handlerConfig, setupErr <-chan error) error {
	allReady := r.awaitAllReady(ctx, handlers)

	var deadlineCh <-chan time.Time
	if r.readyTimeout > 0 {
		timer := time.NewTimer(r.readyTimeout)
		defer timer.Stop()
		deadlineCh = timer.C
	}

	select {
	case <-allReady:
		// All subscriptions are ready.
		return nil
	case err := <-setupErr:
		r.markHealthError(err)
		slog.Error("eventrouter: subscription error during ready wait, shutting down",
			slog.Any("error", err))
		cancel()
		r.wg.Wait()
		// No drain needed: setupErr buffer == len(handlers) guarantees every
		// runSubscribe goroutine can send once without blocking. ctx cancel
		// stops further sends; remaining buffered errors are GC'd with the
		// channel when Run returns.
		return err
	case <-deadlineCh:
		notReady := r.diagnoseNotReady(handlers)
		err := fmt.Errorf("eventrouter: %d/%d subscriptions not ready after %s: %v",
			len(notReady), len(handlers), r.readyTimeout, notReady)
		r.markHealthError(err)
		slog.Error("eventrouter: ready timeout exceeded, shutting down",
			slog.Duration("timeout", r.readyTimeout),
			slog.Int("not_ready_count", len(notReady)),
			slog.Any("not_ready", notReady))
		cancel()
		r.wg.Wait()
		return err
	case <-ctx.Done():
		r.wg.Wait()
		return ctx.Err()
	}
}

// diagnoseNotReady returns "consumerGroup/topic" identifiers for subscriptions
// whose Ready channel has not closed. Used by ready-timeout error reporting
// so operators can see which subscription is stuck without trawling logs.
func (r *Router) diagnoseNotReady(handlers []handlerConfig) []string {
	var notReady []string
	for _, h := range handlers {
		sub := outbox.Subscription{
			Topic:         h.topic,
			ConsumerGroup: h.consumerGroup,
			CellID:        h.consumerGroup,
		}
		select {
		case <-r.subscriber.Ready(sub):
			// ready
		default:
			notReady = append(notReady, fmt.Sprintf("%s/%s", h.consumerGroup, h.topic))
		}
	}
	return notReady
}

// awaitAllReady launches one goroutine per handler that waits on Ready, then
// returns a channel that closes when all goroutines complete (or ctx cancels).
func (r *Router) awaitAllReady(ctx context.Context, handlers []handlerConfig) <-chan struct{} {
	doneCh := make(chan struct{})
	var wg sync.WaitGroup
	for _, h := range handlers {
		h := h
		sub := outbox.Subscription{
			Topic:         h.topic,
			ConsumerGroup: h.consumerGroup,
			CellID:        h.consumerGroup,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-r.subscriber.Ready(sub):
			case <-ctx.Done():
			}
		}()
	}
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	return doneCh
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

// Health reports whether the router is ready to serve subscriptions.
// It returns nil only after startup completes successfully and before a setup
// or runtime failure has been recorded. After graceful shutdown it returns a
// distinguishable "shutting down" error.
func (r *Router) Health() error {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	if r.healthErr != nil {
		return r.healthErr
	}
	if r.shutdown {
		return fmt.Errorf("eventrouter: shutting down")
	}
	if !r.started {
		return fmt.Errorf("eventrouter: not running")
	}
	return nil
}

func (r *Router) markRunning() {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.started = true
	r.healthErr = nil
}

func (r *Router) markHealthError(err error) {
	if err == nil {
		return
	}
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.healthErr = err
}

func (r *Router) markShutdown() {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.shutdown = true
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
