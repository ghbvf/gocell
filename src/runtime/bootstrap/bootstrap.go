// Package bootstrap orchestrates the full GoCell application lifecycle:
// config loading, assembly init/start, HTTP serving, event subscriptions,
// background workers, and graceful shutdown.
//
// ref: uber-go/fx app.go — Run/Start/Stop lifecycle, withRollback pattern
// Adopted: sequential startup with transactional rollback on failure;
// LIFO shutdown order for safe resource cleanup.
// Deviated: explicit typed options instead of DI container; direct signal
// handling via runtime/shutdown.Manager.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/eventrouter"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/ghbvf/gocell/runtime/shutdown"
	"github.com/ghbvf/gocell/runtime/worker"
)

// Option configures a Bootstrap instance.
type Option func(*Bootstrap)

// WithConfig sets the YAML config path and environment prefix.
func WithConfig(yamlPath, envPrefix string) Option {
	return func(b *Bootstrap) {
		b.configPath = yamlPath
		b.envPrefix = envPrefix
	}
}

// WithHTTPAddr sets the HTTP listen address (default ":8080").
func WithHTTPAddr(addr string) Option {
	return func(b *Bootstrap) {
		b.httpAddr = addr
	}
}

// WithAssembly sets a pre-built CoreAssembly.
func WithAssembly(asm *assembly.CoreAssembly) Option {
	return func(b *Bootstrap) {
		b.assembly = asm
	}
}

// WithWorkers adds background workers.
func WithWorkers(ws ...worker.Worker) Option {
	return func(b *Bootstrap) {
		b.workers = append(b.workers, ws...)
	}
}

// WithPublisher sets the outbox.Publisher used for event publishing.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithPublisher(p outbox.Publisher) Option {
	return func(b *Bootstrap) {
		b.publisher = p
	}
}

// WithSubscriber sets the outbox.Subscriber used for event consumption.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithSubscriber(s outbox.Subscriber) Option {
	return func(b *Bootstrap) {
		b.subscriber = s
	}
}

// WithRouterOptions passes options to the router builder.
func WithRouterOptions(opts ...router.Option) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, opts...)
	}
}

// WithTracer enables distributed tracing for HTTP requests. The tracer is
// forwarded to the router's middleware chain via router.WithTracer.
//
// ref: go-zero — observability configuration at app level
func WithTracer(t tracing.Tracer) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, router.WithTracer(t))
	}
}

// WithRateLimiter enables per-IP rate limiting for HTTP requests. The limiter
// is forwarded to the router's middleware chain via router.WithRateLimiter.
//
// ref: go-zero — rate limiting configuration at app level
func WithRateLimiter(rl middleware.RateLimiter) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, router.WithRateLimiter(rl))
	}
}

// WithCircuitBreaker enables circuit breaker protection for HTTP requests.
// The breaker is forwarded to the router's middleware chain via
// router.WithCircuitBreaker.
//
// ref: go-zero — resilience middleware configuration at app level
func WithCircuitBreaker(cb middleware.CircuitBreakerPolicy) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, router.WithCircuitBreaker(cb))
	}
}

// WithShutdownTimeout overrides the default graceful shutdown timeout.
func WithShutdownTimeout(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.shutdownTimeout = d
	}
}

// WithListener sets a pre-built net.Listener for the HTTP server,
// useful in tests to avoid port conflicts.
func WithListener(ln net.Listener) Option {
	return func(b *Bootstrap) {
		b.listener = ln
	}
}

// WithHealthChecker registers a named readiness checker that will be
// included in /readyz responses. Use this to wire adapter health probes
// (e.g., conn.Health for RabbitMQ) without bootstrap depending on adapter types.
//
// Accepts func() error so callers do not need to import runtime/http/health.
func WithHealthChecker(name string, fn func() error) Option {
	if name == "" {
		panic("bootstrap: health checker name must not be empty")
	}
	if fn == nil {
		panic(fmt.Sprintf("bootstrap: health checker %q must not be nil", name))
	}
	return func(b *Bootstrap) {
		b.healthCheckers = append(b.healthCheckers, namedChecker{name: name, fn: fn})
	}
}

// namedChecker pairs a readiness probe name with its check function.
type namedChecker struct {
	name string
	fn   func() error
}

// Bootstrap orchestrates the GoCell application lifecycle.
type Bootstrap struct {
	configPath      string
	envPrefix       string
	httpAddr        string
	assembly        *assembly.CoreAssembly
	workers         []worker.Worker
	publisher       outbox.Publisher
	subscriber      outbox.Subscriber
	routerOpts      []router.Option
	shutdownTimeout time.Duration
	listener        net.Listener
	healthCheckers  []namedChecker
}

// New creates a Bootstrap with the given options.
func New(opts ...Option) *Bootstrap {
	b := &Bootstrap{
		httpAddr:        ":8080",
		shutdownTimeout: shutdown.DefaultTimeout,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Run executes the full startup sequence. It blocks until ctx is cancelled
// (or a signal is received), then performs orderly shutdown.
//
// Startup sequence (ref: uber-go/fx app.go Run):
//  1. Load config
//  2. Initialise publisher/subscriber (default: InMemoryEventBus for both)
//  3. Initialise assembly (inject config into Dependencies.Config)
//  4. Cell.Init -> Cell.Start (assembly.Start)
//  5. RegisterRoutes for HTTPRegistrar cells
//  6. RegisterSubscriptions for EventRegistrar cells
//  7. Start HTTP server
//  8. Start workers
//  9. Wait for signal (runtime/shutdown)
//  10. Shutdown: stop workers -> drain HTTP -> stop assembly -> close subscriber/publisher
//
// If any step fails, already-started components are rolled back in reverse.
func (b *Bootstrap) Run(ctx context.Context) error {
	// Track teardown functions for rollback (LIFO order).
	var teardowns []func(context.Context) error

	rollback := func(cause error) error {
		slog.Error("bootstrap: startup failed, rolling back", slog.Any("error", cause))
		rctx, cancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
		defer cancel()
		for i := len(teardowns) - 1; i >= 0; i-- {
			if err := teardowns[i](rctx); err != nil {
				slog.Warn("bootstrap: rollback step failed", slog.Any("error", err))
			}
		}
		return cause
	}

	// Step 1: Load config.
	var cfg config.Config
	if b.configPath != "" {
		var err error
		cfg, err = config.Load(b.configPath, b.envPrefix)
		if err != nil {
			return fmt.Errorf("bootstrap: load config: %w", err)
		}
	} else {
		cfg = config.NewFromMap(make(map[string]any))
	}

	// Step 1.5a: Create config watcher (if config file provided).
	// The watcher is created here but NOT started until Step 4.5, after the
	// OnChange callback is registered. This prevents a startup window where
	// file events are consumed but no callback is bound to handle them.
	var cfgWatcher *config.Watcher
	if b.configPath != "" {
		w, err := config.NewWatcher(b.configPath)
		if err != nil {
			slog.Warn("bootstrap: config watcher not available", slog.Any("error", err))
		} else {
			cfgWatcher = w
			teardowns = append(teardowns, func(_ context.Context) error {
				return cfgWatcher.Close()
			})
		}
	}

	// Step 2: Initialise publisher and subscriber.
	// If neither publisher nor subscriber is set, create a default InMemoryEventBus
	// that satisfies both roles — preserving the original single-bus behaviour.
	pub := b.publisher
	sub := b.subscriber
	if pub == nil && sub == nil {
		eb := eventbus.New()
		pub = eb
		sub = eb
	}
	// Register teardown for subscriber (if it implements io.Closer).
	if cl, ok := sub.(io.Closer); ok {
		teardowns = append(teardowns, func(_ context.Context) error {
			return cl.Close()
		})
	}
	// Register teardown for publisher (if it implements io.Closer and is not
	// the same instance as the subscriber — avoid double-close).
	if cl, ok := pub.(io.Closer); ok && any(pub) != any(sub) {
		teardowns = append(teardowns, func(_ context.Context) error {
			return cl.Close()
		})
	}

	// Step 3-4: Initialise and start assembly.
	asm := b.assembly
	if asm == nil {
		asm = assembly.New(assembly.Config{ID: "default"})
	}

	// Inject config into assembly dependencies.
	cfgMap := snapshotConfig(cfg)

	if err := asm.StartWithConfig(ctx, cfgMap); err != nil {
		return rollback(fmt.Errorf("bootstrap: assembly start: %w", err))
	}
	// assemblyStopped + reloadWG together ensure clean shutdown of the reload
	// pipeline. The guard prevents new callbacks from entering after shutdown
	// begins; the WaitGroup drains any in-flight callback that passed the
	// guard before assemblyStopped was set. Teardown sequence:
	//   1. assemblyStopped.Store(true) — stop new callbacks
	//   2. reloadWG.Wait()             — drain in-flight callbacks
	//   3. asm.Stop(c)                 — safe: no concurrent OnConfigReload
	//
	// ref: net/http Server.Shutdown — stop accepting + drain active + close.
	var assemblyStopped atomic.Bool
	var reloadWG sync.WaitGroup

	teardowns = append(teardowns, func(c context.Context) error {
		assemblyStopped.Store(true)
		reloadWG.Wait()
		return asm.Stop(c)
	})

	// Step 4.5: Register config watcher OnChange callback (now that asm is started).
	// Snapshot → Reload → Diff → notify ConfigReloader cells.
	if cfgWatcher != nil {
		yamlPath, envPrefix := b.configPath, b.envPrefix
		cfgWatcher.OnChange(func(evt config.WatchEvent) {
			if assemblyStopped.Load() {
				return
			}
			reloadWG.Add(1)
			defer reloadWG.Done()
			// Double-check after Add: if shutdown raced between the Load above
			// and Add, we must not proceed.
			if assemblyStopped.Load() {
				return
			}

			rc, ok := cfg.(config.Reloader)
			if !ok {
				return
			}

			oldSnap := snapshotConfig(cfg)

			if err := rc.Reload(yamlPath, envPrefix); err != nil {
				slog.Error("bootstrap: config reload failed", slog.Any("error", err))
				return
			}
			slog.Info("bootstrap: config reloaded", slog.String("path", evt.Path))

			newSnap := snapshotConfig(cfg)
			added, updated, removed := config.Diff(oldSnap, newSnap)
			if len(added) == 0 && len(updated) == 0 && len(removed) == 0 {
				slog.Debug("bootstrap: config reloaded but no effective changes")
				return
			}

			// Read config generation for tracking drift between config and cells.
			var gen int64
			if g, ok := cfg.(config.Generationer); ok {
				gen = g.Generation()
			}

			for _, id := range asm.CellIDs() {
				c := asm.Cell(id)
				cr, ok := c.(cell.ConfigReloader)
				if !ok {
					continue
				}
				// Clone per cell to guarantee isolation: a misbehaving handler
				// cannot mutate slices/map seen by subsequent handlers.
				event := cell.ConfigChangeEvent{
					Added:      cloneStrings(added),
					Updated:    cloneStrings(updated),
					Removed:    cloneStrings(removed),
					Config:     cloneMap(newSnap),
					Generation: gen,
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							slog.Error("bootstrap: config reload callback panic",
								slog.String("cell", id),
								slog.String("type", fmt.Sprintf("%T", r)))
							slog.Debug("bootstrap: config reload callback panic detail",
								slog.String("cell", id), slog.Any("panic", r))
						}
					}()
					if err := cr.OnConfigReload(event); err != nil {
						slog.Error("bootstrap: config reload callback failed",
							slog.String("cell", id),
							slog.Any("error", err),
							slog.Int64("config_generation", gen))
					}
				}()
			}
		})
		// Start after OnChange is bound so no events are consumed without a handler.
		cfgWatcher.Start()
	}

	// Step 5: Build router with health handler.
	// Use NewE (error-returning) so that configuration errors (e.g. invalid
	// trusted proxies) enter the rollback path instead of panicking past
	// already-started components (assembly, config watcher, pub/sub).
	//
	// ref: uber-go/fx — startup failures return error, trigger rollback
	hh := health.New(asm)
	for _, hc := range b.healthCheckers {
		hh.RegisterChecker(hc.name, health.Checker(hc.fn))
	}
	routerOpts := append([]router.Option{router.WithHealthHandler(hh)}, b.routerOpts...)
	rtr, err := router.NewE(routerOpts...)
	if err != nil {
		return rollback(fmt.Errorf("bootstrap: %w", err))
	}

	// Step 5 continued: Register HTTP routes for cells implementing HTTPRegistrar.
	for _, id := range asm.CellIDs() {
		c := asm.Cell(id)
		if hr, ok := c.(cell.HTTPRegistrar); ok {
			hr.RegisterRoutes(rtr)
		}
	}

	// Step 6: Register event subscriptions via EventRouter.
	// Cells declare handlers (non-blocking), then Router.Run starts consumption.
	// Setup errors (e.g., missing DLX) abort startup.
	//
	// Invariant: if any cell declares subscriptions, a subscriber must be injected.
	// Without this check, callers who migrate from WithEventBus to WithPublisher
	// but forget WithSubscriber would silently lose all event consumption.
	var routerErrCh chan error // hoisted for Step 9 monitoring
	if sub == nil {
		// Check whether any cell implements EventRegistrar — if so, the missing
		// subscriber is a configuration error, not a valid "no-events" setup.
		for _, id := range asm.CellIDs() {
			if _, ok := asm.Cell(id).(cell.EventRegistrar); ok {
				return rollback(fmt.Errorf(
					"bootstrap: cell %s implements EventRegistrar but no subscriber is configured; "+
						"add WithSubscriber to bootstrap options", id))
			}
		}
	}
	if sub != nil {
		evtRouter := eventrouter.New(sub)
		for _, id := range asm.CellIDs() {
			c := asm.Cell(id)
			if er, ok := c.(cell.EventRegistrar); ok {
				if err := er.RegisterSubscriptions(evtRouter); err != nil {
					return rollback(fmt.Errorf("bootstrap: cell %s subscription setup failed: %w", id, err))
				}
			}
		}
		if evtRouter.HandlerCount() > 0 {
			slog.Info("bootstrap: starting event router",
				slog.Int("handler_count", evtRouter.HandlerCount()))
			routerErrCh = make(chan error, 1)
			go func() {
				routerErrCh <- evtRouter.Run(ctx)
			}()
			// Wait for all subscriptions to start or a setup error.
			select {
			case err := <-routerErrCh:
				return rollback(fmt.Errorf("bootstrap: event router: %w", err))
			case <-evtRouter.Running():
				// All subscriptions consuming.
			}
			teardowns = append(teardowns, func(c context.Context) error {
				return evtRouter.Close(c)
			})
		}
	}

	// Step 7: Start HTTP server.
	srv := &http.Server{
		Addr:              b.httpAddr,
		Handler:           rtr,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln := b.listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", b.httpAddr)
		if err != nil {
			return rollback(fmt.Errorf("bootstrap: listen %s: %w", b.httpAddr, err))
		}
	}

	httpErrCh := make(chan error, 1)
	go func() {
		slog.Info("bootstrap: HTTP server starting", slog.String("addr", ln.Addr().String()))
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErrCh <- err
		}
		close(httpErrCh)
	}()
	teardowns = append(teardowns, func(c context.Context) error {
		slog.Info("bootstrap: draining HTTP server")
		return srv.Shutdown(c)
	})

	// Step 8: Start workers.
	wg := worker.NewWorkerGroup()
	for _, w := range b.workers {
		wg.Add(w)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	workerErrCh := make(chan error, 1)
	if len(b.workers) > 0 {
		go func() {
			workerErrCh <- wg.Start(workerCtx)
			close(workerErrCh)
		}()
		teardowns = append(teardowns, func(c context.Context) error {
			workerCancel()
			return wg.Stop(c)
		})
	} else {
		workerCancel() // no workers, release the context
	}

	// Step 9: Wait for shutdown signal or error.
	// Monitor all background components: HTTP, workers, and event router.
	slog.Info("bootstrap: application started successfully")
	select {
	case <-ctx.Done():
		slog.Info("bootstrap: context cancelled, shutting down")
	case err := <-httpErrCh:
		if err != nil {
			return rollback(fmt.Errorf("bootstrap: http server: %w", err))
		}
	case err := <-workerErrCh:
		if err != nil {
			slog.Error("bootstrap: worker failed, initiating shutdown", slog.Any("error", err))
			return rollback(fmt.Errorf("bootstrap: worker: %w", err))
		}
	case err := <-routerErrCh:
		if err != nil {
			slog.Error("bootstrap: event router failed, initiating shutdown", slog.Any("error", err))
			return rollback(fmt.Errorf("bootstrap: event router: %w", err))
		}
	}

	// Step 10: Orderly shutdown.
	slog.Info("bootstrap: initiating graceful shutdown")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
	defer shutCancel()

	var errs []error
	for i := len(teardowns) - 1; i >= 0; i-- {
		if err := teardowns[i](shutCtx); err != nil {
			slog.Error("bootstrap: shutdown step failed", slog.Any("error", err))
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// cloneStrings returns a shallow copy of a string slice.
// If src is nil, returns nil (preserving the nil vs empty distinction).
func cloneStrings(src []string) []string {
	if src == nil {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

// cloneMap returns a deep copy of a map[string]any. Values that are slices
// or nested maps are recursively cloned so that mutations by one consumer
// cannot affect another.
func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = config.DeepCloneValue(v)
	}
	return dst
}

// snapshotConfig builds an atomic point-in-time copy of the config.
// If the config implements Snapshotter (the concrete *config from Load does),
// the snapshot is taken under a single read lock for consistency. Otherwise,
// it falls back to iterating Keys()+Get() which is non-atomic but functional.
func snapshotConfig(cfg config.Config) map[string]any {
	if s, ok := cfg.(config.Snapshotter); ok {
		return s.Snapshot()
	}
	snap := make(map[string]any)
	for _, k := range cfg.Keys() {
		snap[k] = cfg.Get(k)
	}
	return snap
}
