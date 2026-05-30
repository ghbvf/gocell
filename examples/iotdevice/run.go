// run.go is the hand-written runtime half behind the generated assembly
// entrypoint for iotdevice. The generated main.go owns the assembly ID and
// cell order; this file owns environment loading and runtime option wiring.
//
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

	"github.com/ghbvf/gocell/kernel/auth"

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
)

// runIotdevice is the hand-written runtime helper for the iotdevice assembly.
// It is called by the generated main.go and owns environment loading +
// bootstrap wiring.
func runIotdevice(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	mods, err := runIotdeviceModules(assemblyID, assemblyCellIDs)
	if err != nil {
		return err
	}
	_ = mods // cell construction is done directly below; mods only validates drift

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	internalAuthChain, err := newInternalAuthChainFromEnv()
	if err != nil {
		return fmt.Errorf("configure internal listener auth: %w", err)
	}
	jwtVerifier, err := newJWTVerifierFromEnv()
	if err != nil {
		return fmt.Errorf("configure JWT verifier: %w", err)
	}

	// Single root clock shared by assembly, bootstrap, eventbus, and cell.
	// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
	clk := clock.Real()

	// In-memory event bus for demo mode.
	eb := eventbus.New(clk)

	// Event publish channel selection. By default the cell publishes
	// device-registered events to the in-memory bus (no in-process subscriber —
	// it is a demo sink). When GOCELL_IOTDEVICE_MQTT_BROKERS is set, swap the
	// cell's direct publisher to MQTT so events flow to a real broker, observable
	// with `mosquitto_sub`. This is a single-channel swap, not a parallel mirror:
	// device-registered has no second sink to mirror to. The HTTP/WS main path is
	// unchanged. See examples/iotdevice/docs/mqtt.md.
	var directPub outbox.Publisher = eb
	var mqttBootstrapOpts []bootstrap.Option
	mqttPub, mqttConn, mqttOK, err := buildMQTTDirectPublisher(ctx, clk, logger)
	if err != nil {
		return fmt.Errorf("build mqtt publish channel: %w", err)
	}
	if mqttOK {
		directPub = mqttPub
		// Connection implements lifecycle.ManagedResource: WithManagedResource
		// registers its mqtt_ready readiness probe (with the default probe
		// timeout) + LIFO Close in one call — the standard adapter wiring path,
		// matching postgres/rabbitmq/redis.
		mqttBootstrapOpts = append(mqttBootstrapOpts,
			bootstrap.WithManagedResource(mqttConn),
		)
	}

	// Resolve persistence: durable PG wiring when GOCELL_IOTDEVICE_DSN is set,
	// otherwise explicit in-memory wiring. The cell never falls back silently
	// (B2.B: no soft fallback), so demo runs MUST inject mem implementations.
	// In durable mode, pgPool is non-nil and is registered as a
	// bootstrap.WithManagedCloser below so framework LIFO teardown closes it
	// during shutdown — defer-based cleanup would be skipped on os.Exit(1).
	deviceRepo, commandQueue, durabilityMode, pgPool, err := buildDevicePersistence(ctx, clk, logger)
	if err != nil {
		return fmt.Errorf("build device persistence: %w", err)
	}

	// Cursor codec for pagination. Durable mode requires a real key from the
	// environment; demo mode uses a hard-coded public key (not production-safe).
	cursorCodec, err := buildCursorCodec(durabilityMode)
	if err != nil {
		return fmt.Errorf("build cursor codec: %w", err)
	}

	// Create the device cell with explicitly wired persistence.
	dc := devicecell.NewDeviceCell(
		clk,
		devicecell.WithDeviceRepository(deviceRepo),
		devicecell.WithDirectPublisher(outbox.WrapPublisherForCell(directPub)),
		devicecell.WithCursorCodec(cursorCodec),
		devicecell.WithLogger(logger),
	)
	dc.RegisterCommandQueue(commandQueue)

	// Build assembly and register the cell.
	asm := assembly.New(clk, assembly.Config{ID: assemblyID, DurabilityMode: durabilityMode})
	if err := asm.Register(dc); err != nil {
		return fmt.Errorf("register devicecell: %w", err)
	}

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

	jwtPlan, err := auth.NewAuthJWT(jwtVerifier)
	if err != nil {
		return fmt.Errorf("invalid JWT auth plan: %w", err)
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithListener(cell.PrimaryListener, ":8083", []auth.ListenerAuth{jwtPlan}),
		bootstrap.WithListener(cell.InternalListener, ":9083", internalAuthChain),
		// #673: a dedicated HealthListener is mandatory — /healthz, /readyz,
		// /metrics no longer fall back onto the primary listener.
		bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9093", []auth.ListenerAuth{auth.AuthNone{}}),
		bootstrap.WithHealthRoutes(healthOpts...),
	}
	// MQTT channel options (health probe + managed closer) when enabled.
	opts = append(opts, mqttBootstrapOpts...)
	// Durable mode: register the PG pool as a managed closer so framework
	// LIFO teardown closes it even when app.Run returns an error followed by
	// os.Exit(1). Defer-based cleanup would be skipped on that path.
	if pgPool != nil {
		opts = append(opts, bootstrap.WithManagedCloser(pgPool))
	}
	app := bootstrap.New(clk, opts...)

	logger.Info("iotdevice: starting on :8083; protected routes require an RS256 bearer token")
	return app.Run(ctx)
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
	devicepg.DeviceRepository, deviceCommandQueue, outbox.DurabilityMode, *adapterpg.Pool, error,
) {
	dsn := os.Getenv("GOCELL_IOTDEVICE_DSN")
	if dsn == "" {
		logger.Info("iotdevice: using in-memory persistence (set GOCELL_IOTDEVICE_DSN for PG)")
		// nil pool is the documented "demo mode" signal — runIotdevice branches on
		// pool != nil to decide WithManagedCloser. err is also nil because
		// demo wiring cannot fail. Linter conventionally treats (nil, nil) as
		// ambiguous, but here the pool channel is a multi-return discriminant.
		//nolint:nilnil // demo mode returns nil pool intentionally; see godoc
		return devicemem.NewDeviceRepository(), commandtest.NewInMemQueue(), outbox.DurabilityDemo, nil, nil
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
	return deviceRepo, commandQueue, outbox.DurabilityDurable, pool, nil
}

// buildCursorCodec builds a CursorCodec appropriate for the durability mode.
// Durable mode requires GOCELL_IOTDEVICE_CURSOR_KEY env var (≥32 bytes) so
// that the hard-coded demo key is never used in production.
// Demo mode uses a well-known public key that is intentionally NOT secret.
func buildCursorCodec(mode outbox.DurabilityMode) (*query.CursorCodec, error) {
	if mode == outbox.DurabilityDurable {
		raw := os.Getenv("GOCELL_IOTDEVICE_CURSOR_KEY")
		if len(raw) < 32 {
			return nil, errors.New("GOCELL_IOTDEVICE_CURSOR_KEY env var required (>=32 bytes) in durable mode")
		}
		return query.NewCursorCodec([]byte(raw))
	}
	return query.NewCursorCodec([]byte("iotdevice-cursor-key-32-bytes!!!"))
}

// runIotdeviceModules validates that assembly.yaml cells (assemblyCellIDs)
// match the generated module list in modules_gen.go. A mismatch means
// `gocell generate assembly --id=iotdevice` has not been re-run after an
// assembly.yaml change.
func runIotdeviceModules(assemblyID string, cellIDs []string) ([]CellModule, error) {
	mods := generatedCellModules()
	if err := assertModuleIDsMatch(assemblyID, cellIDs, mods); err != nil {
		return nil, err
	}
	return mods, nil
}

// assertModuleIDsMatch fails-fast when assembly.yaml.cells (cellIDs) drifts
// from the generated module list. The two should be 1:1 in declaration order;
// any mismatch indicates a missing `gocell generate assembly` run.
func assertModuleIDsMatch(assemblyID string, cellIDs []string, mods []CellModule) error {
	hint := fmt.Sprintf("run `gocell generate assembly --id=%s`", assemblyID)
	if len(cellIDs) != len(mods) {
		return fmt.Errorf(
			"%s: assembly.yaml cells (%d) ↔ modules_gen.go (%d) length mismatch; %s",
			assemblyID, len(cellIDs), len(mods), hint)
	}
	for i, want := range cellIDs {
		if got := mods[i].ID(); got != want {
			return fmt.Errorf(
				"%s: assembly.yaml cells[%d]=%q ↔ modules_gen.go=%q drift; %s",
				assemblyID, i, want, got, hint)
		}
	}
	return nil
}

// CellModule is the K#10 modules_gen.go interface contract: each generated
// factory returns a value implementing ID(). The iotdevice assembly uses
// stub module values only for drift detection; cell construction is done
// directly in runIotdevice.
type CellModule interface {
	ID() string
}

// DeviceCellModule is the stub module for devicecell — returned by
// generatedCellModules() in modules_gen.go. It carries the cell ID for the
// assembly drift check and does not participate in Provide wiring (direct
// cell construction is used for this example assembly).
type DeviceCellModule struct{}

// ID returns the devicecell identifier.
func (DeviceCellModule) ID() string { return "devicecell" }
