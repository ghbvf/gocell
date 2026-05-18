// Package main is the entry point for the iotdevice example application.
// It demonstrates the L4 DeviceLatent consistency model: commands are enqueued
// by the server and polled by IoT devices on their own schedule.
//
// Usage:
//
//	go run ./examples/iotdevice
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	devicecell "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell"
	devicemem "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/mem"
	devicepg "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/postgres"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/shutdown"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	internalAuthChain, err := newInternalAuthChainFromEnv()
	if err != nil {
		logger.Error("failed to configure internal listener auth", slog.Any("error", err))
		os.Exit(1)
	}
	jwtVerifier, err := newJWTVerifierFromEnv()
	if err != nil {
		logger.Error("failed to configure JWT verifier", slog.Any("error", err))
		os.Exit(1)
	}

	// Single root clock shared by assembly, bootstrap, eventbus, and cell.
	// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
	clk := clock.Real()

	// In-memory event bus for demo mode.
	eb := eventbus.New(eventbus.WithClock(clk))

	// Resolve persistence: durable PG wiring when GOCELL_IOTDEVICE_DSN is set,
	// otherwise explicit in-memory wiring. The cell never falls back silently
	// (B2.B: no soft fallback), so demo runs MUST inject mem implementations.
	// In durable mode, pgPool is non-nil and is registered as a
	// bootstrap.WithManagedCloser below so framework LIFO teardown closes it
	// during shutdown — defer-based cleanup would be skipped on os.Exit(1).
	deviceRepo, commandQueue, durabilityMode, pgPool, err := buildDevicePersistence(context.Background(), clk, logger)
	if err != nil {
		logger.Error("failed to build device persistence", slog.Any("error", err))
		os.Exit(1)
	}

	// Cursor codec for pagination. Durable mode requires a real key from the
	// environment; demo mode uses a hard-coded public key (not production-safe).
	cursorCodec, err := buildCursorCodec(durabilityMode)
	if err != nil {
		logger.Error("failed to build cursor codec", slog.Any("error", err))
		os.Exit(1)
	}

	// Create the device cell with explicitly wired persistence.
	dc := devicecell.NewDeviceCell(
		devicecell.WithClock(clk),
		devicecell.WithDeviceRepository(deviceRepo),
		devicecell.WithDirectPublisher(outbox.WrapPublisherForCell(eb)),
		devicecell.WithCursorCodec(cursorCodec),
		devicecell.WithLogger(logger),
	)
	dc.RegisterCommandQueue(commandQueue)

	// Build assembly and register the cell.
	asm := assembly.New(assembly.Config{ID: "iotdevice", DurabilityMode: durabilityMode, Clock: clk})
	if err := asm.Register(dc); err != nil {
		logger.Error("failed to register devicecell", slog.Any("error", err))
		os.Exit(1)
	}

	// Bootstrap the application on :8083.
	ctx, stop := shutdown.NotifyContext(context.Background())
	defer stop()

	// PR-A35 + PR269 round-3: /readyz?verbose is gated by the health handler's
	// strict X-Readyz-Token check. When the operator sets
	// GOCELL_READYZ_VERBOSE_TOKEN, plumb it via WithReadyzVerboseToken;
	// otherwise waive the verbose endpoint via WithReadyzVerboseDisabled so the
	// demo binary keeps starting out of the box without exposing internal
	// topology anonymously.
	healthOpts := []bootstrap.HealthRouteGroupOption{}
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		healthOpts = append(healthOpts, bootstrap.WithReadyzVerboseToken(tok))
	} else {
		healthOpts = append(healthOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	jwtPlan, err := cell.NewAuthJWT(jwtVerifier)
	if err != nil {
		logger.Error("iotdevice: invalid JWT auth plan", slog.Any("error", err))
		os.Exit(1)
	}

	opts := []bootstrap.Option{
		bootstrap.WithClock(clk),
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithListener(cell.PrimaryListener, ":8083", []cell.ListenerAuth{jwtPlan}),
		bootstrap.WithListener(cell.InternalListener, ":9083", internalAuthChain),
		bootstrap.WithHealthRoutes(healthOpts...),
	}
	// Durable mode: register the PG pool as a managed closer so framework
	// LIFO teardown closes it even when app.Run returns an error followed by
	// os.Exit(1). Defer-based cleanup would be skipped on that path.
	if pgPool != nil {
		opts = append(opts, bootstrap.WithManagedCloser(pgPool))
	}
	app := bootstrap.New(opts...)

	logger.Info("iotdevice: starting on :8083; protected routes require an RS256 bearer token")
	if err := app.Run(ctx); err != nil {
		logger.Error("iotdevice: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

// deviceCommandQueue is the runtime contract devicecell expects — a single
// store implements both kernel/command.Queue (consumer path) and
// command.ActiveScanner (sweeper / ops view).
type deviceCommandQueue interface {
	kcommand.Queue
	kcommand.ActiveScanner
}

// buildDevicePersistence resolves the device repository + command queue based
// on GOCELL_IOTDEVICE_DSN. When the DSN env var is set, durable mode wires PG
// implementations + applies migrations; otherwise demo mode wires the
// in-memory implementations.
//
// The returned `*adapterpg.Pool` is nil in demo mode; in durable mode the
// caller MUST register the pool via `bootstrap.WithManagedCloser(pool)` so
// the framework's LIFO teardown closes it during shutdown — relying on a
// deferred `pool.Close()` would be skipped when `os.Exit(1)` runs after
// `app.Run` returns an error.
func buildDevicePersistence(ctx context.Context, clk clock.Clock, logger *slog.Logger) (
	devicepg.DeviceRepository, deviceCommandQueue, cell.DurabilityMode, *adapterpg.Pool, error,
) {
	dsn := os.Getenv("GOCELL_IOTDEVICE_DSN")
	if dsn == "" {
		logger.Info("iotdevice: using in-memory persistence (set GOCELL_IOTDEVICE_DSN for PG)")
		// nil pool is the documented "demo mode" signal — main.go branches on
		// pool != nil to decide WithManagedCloser. err is also nil because
		// demo wiring cannot fail. Linter conventionally treats (nil, nil) as
		// ambiguous, but here the pool channel is a multi-return discriminant.
		//nolint:nilnil // demo mode returns nil pool intentionally; see godoc
		return devicemem.NewDeviceRepository(), commandtest.NewInMemQueue(), cell.DurabilityDemo, nil, nil
	}

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	if err != nil {
		return nil, nil, 0, nil, fmt.Errorf("pg pool: %w", err)
	}
	closePool := func() {
		if err := pool.Close(ctx); err != nil {
			logger.Warn("iotdevice: pg pool close failed", slog.Any("error", err))
		}
	}

	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("migrations fs: %w", err)
	}
	migrator, err := adapterpg.NewMigrator(pool, migrationsFS, "schema_migrations")
	if err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("migrator: %w", err)
	}
	if err := migrator.Up(ctx); err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("migrate up: %w", err)
	}
	logger.Info("iotdevice: migrations applied")

	if err := adapterpg.VerifyExpectedVersion(ctx, pool, migrationsFS, "schema_migrations"); err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("schema version verify: %w", err)
	}
	if err := adapterpg.VerifyExpectedShape(ctx, pool); err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("schema shape verify: %w", err)
	}
	if err := adapterpg.VerifyNoInvalidIndexes(ctx, pool); err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("schema indexes verify: %w", err)
	}
	logger.Info("iotdevice: schema verified")

	txMgr := adapterpg.NewTxManager(pool)
	deviceRepo, err := devicepg.NewDeviceRepository(pool.DB(), txMgr, clk)
	if err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("device repo: %w", err)
	}
	commandQueue, err := adapterpg.NewCommandQueue(pool.DB(), txMgr, clk)
	if err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("command queue: %w", err)
	}

	logger.Info("iotdevice: using PG persistence (durable mode)", slog.String("dsn", "set"))
	return deviceRepo, commandQueue, cell.DurabilityDurable, pool, nil
}

// buildCursorCodec builds a CursorCodec appropriate for the durability mode.
// Durable mode requires GOCELL_IOTDEVICE_CURSOR_KEY env var (≥32 bytes) so
// that the hard-coded demo key is never used in production.
// Demo mode uses a well-known public key that is intentionally NOT secret.
func buildCursorCodec(mode cell.DurabilityMode) (*query.CursorCodec, error) {
	if mode == cell.DurabilityDurable {
		raw := os.Getenv("GOCELL_IOTDEVICE_CURSOR_KEY")
		if len(raw) < 32 {
			return nil, errors.New("GOCELL_IOTDEVICE_CURSOR_KEY env var required (>=32 bytes) in durable mode")
		}
		return query.NewCursorCodec([]byte(raw))
	}
	return query.NewCursorCodec([]byte("iotdevice-cursor-key-32-bytes!!!"))
}
