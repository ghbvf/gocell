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
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
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

// WithEventBus sets a pre-built InMemoryEventBus.
func WithEventBus(eb *eventbus.InMemoryEventBus) Option {
	return func(b *Bootstrap) {
		b.eventBus = eb
	}
}

// WithRouterOptions passes options to the router builder.
func WithRouterOptions(opts ...router.Option) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, opts...)
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

// Bootstrap orchestrates the GoCell application lifecycle.
type Bootstrap struct {
	configPath      string
	envPrefix       string
	httpAddr        string
	assembly        *assembly.CoreAssembly
	workers         []worker.Worker
	eventBus        *eventbus.InMemoryEventBus
	routerOpts      []router.Option
	shutdownTimeout time.Duration
	listener        net.Listener
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
//  2. Initialise eventbus
//  3. Initialise assembly (inject config into Dependencies.Config)
//  4. Cell.Init -> Cell.Start (assembly.Start)
//  5. RegisterRoutes for HTTPRegistrar cells
//  6. RegisterSubscriptions for EventRegistrar cells
//  7. Start HTTP server
//  8. Start workers
//  9. Wait for signal (runtime/shutdown)
//  10. Shutdown: stop workers -> drain HTTP -> stop assembly -> close eventbus
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

	// Step 2: Initialise eventbus.
	eb := b.eventBus
	if eb == nil {
		eb = eventbus.New()
	}
	teardowns = append(teardowns, func(_ context.Context) error {
		return eb.Close()
	})

	// Step 3-4: Initialise and start assembly.
	asm := b.assembly
	if asm == nil {
		asm = assembly.New(assembly.Config{ID: "default"})
	}

	// Inject config into assembly dependencies.
	cfgMap := make(map[string]any)
	for _, k := range cfg.Keys() {
		cfgMap[k] = cfg.Get(k)
	}

	if err := asm.StartWithConfig(ctx, cfgMap); err != nil {
		return rollback(fmt.Errorf("bootstrap: assembly start: %w", err))
	}
	teardowns = append(teardowns, func(c context.Context) error {
		return asm.Stop(c)
	})

	// Step 5: Build router with health handler.
	hh := health.New(asm)
	routerOpts := append([]router.Option{router.WithHealthHandler(hh)}, b.routerOpts...)
	rtr := router.New(routerOpts...)

	// Step 5 continued: Register HTTP routes for cells implementing HTTPRegistrar.
	for _, id := range asm.CellIDs() {
		c := asm.Cell(id)
		if hr, ok := c.(cell.HTTPRegistrar); ok {
			hr.RegisterRoutes(rtr)
		}
	}

	// Step 6: Register event subscriptions for cells implementing EventRegistrar.
	for _, id := range asm.CellIDs() {
		c := asm.Cell(id)
		if er, ok := c.(cell.EventRegistrar); ok {
			er.RegisterSubscriptions(eb)
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
	}

	// Step 10: Orderly shutdown.
	slog.Info("bootstrap: initiating graceful shutdown")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
	defer shutCancel()

	var firstErr error
	for i := len(teardowns) - 1; i >= 0; i-- {
		if err := teardowns[i](shutCtx); err != nil {
			slog.Error("bootstrap: shutdown step failed", slog.Any("error", err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
