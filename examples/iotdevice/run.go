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

	"github.com/ghbvf/gocell/framework/kernel/auth"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	devicecell "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell"
	devicemem "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/mem"
	devicepg "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/postgres"
	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	kcommand "github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/kernel/command/commandtest"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/migration"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	commandruntime "github.com/ghbvf/gocell/framework/runtime/command"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	rtmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	outboxruntime "github.com/ghbvf/gocell/framework/runtime/outbox"
	"github.com/ghbvf/gocell/framework/runtime/outbox/outboxtest"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
)

const envDurableSinglePod = "GOCELL_IOTDEVICE_DURABLE_SINGLE_POD"

// runIotdevice is the hand-written runtime helper for the iotdevice assembly.
// It is called by the generated main.go and owns environment loading +
// bootstrap wiring.
func runIotdevice(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	// The redacting slog default is sealed by the generated main.go's run()
	// (SLOG-HANDLER-SEALED-FUNNEL-01 A3 generated segment) before this function
	// runs, so logger (and every slog.Default() call) is already scrubbed.
	logger := slog.Default()

	mods, err := runIotdeviceModules(assemblyID, assemblyCellIDs)
	if err != nil {
		return err
	}
	_ = mods // cell construction is done directly below; mods only validates drift

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

	// Event publish channel selection. The cell always publishes device-registered
	// events to the in-memory bus, where the in-process devicebootstrap subscriber
	// reactively consumes them and enqueues a bootstrap command (the #1698 reactive
	// loop). When GOCELL_IOTDEVICE_MQTT_BROKERS is set, directPub becomes a tee:
	// local eb first, plus MQTT as an external observable mirror for mosquitto_sub.
	// The HTTP/WS main path is unchanged. See examples/iotdevice/docs/mqtt.md.
	var directPub outbox.Publisher = eb
	var mqttBootstrapOpts []bootstrap.Option
	mqttPub, mqttConn, mqttOK, err := buildMQTTDirectPublisher(ctx, clk, logger)
	if err != nil {
		return fmt.Errorf("build mqtt publish channel: %w", err)
	}
	if mqttOK {
		directPub = &teePublisher{local: eb, external: mqttPub}
		// Register BOTH the connection (managed resource: mqtt_ready probe +
		// disconnect) AND the publisher (managed closer: drains in-flight
		// publishes). Connection.Close only disconnects — it does NOT drain, so
		// the publisher closer is mandatory, not redundant (PR #1364 review F1).
		// mqttChannelWiringFor derives both; its godoc documents the LIFO
		// drain-before-disconnect ordering.
		mqttBootstrapOpts = append(
			mqttBootstrapOpts,
			mqttChannelWiringFor(mqttPub, mqttConn).bootstrapOptions()...,
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

	// Process command.Registry for the synchronous command bus. devicecell
	// registers its enqueue handler into it during Init; the registry is a
	// required cell dependency (#1580), so it must be wired here.
	commandReg := commandruntime.NewRegistry()

	// Async command-relay subsystem (#1698): writer-backed bootstrap emitter +
	// ConsumerBase (idempotency guard for the device-registered subscriber) +
	// relay (polls the outbox store, in-process-dispatches command entries). Built
	// for both demo and durable modes; pgPool discriminates the backing store.
	crs, err := buildCommandRelaySubsystem(clk, eb, commandReg, pgPool)
	if err != nil {
		return fmt.Errorf("build command-relay subsystem: %w", err)
	}

	// Create the device cell with explicitly wired persistence.
	dc := devicecell.NewDeviceCell(
		clk,
		devicecell.WithDeviceRepository(deviceRepo),
		devicecell.WithDirectPublisher(outbox.WrapPublisherForCell(directPub)),
		devicecell.WithBootstrapEmitter(crs.bootstrapEmitter),
		devicecell.WithBootstrapTxManager(crs.bootstrapTxManager),
		devicecell.WithCursorCodec(cursorCodec),
		devicecell.WithCommandRegistry(commandReg),
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

	// gRPC listener (first end-to-end grpc handler, #1151). devicecell registers
	// grpc.device.command.v1 on cell.PrimaryListener (cell_gen.go reg.GRPCService),
	// so a grpc listener with that ref MUST be wired or bootstrap phase7b fails
	// fast.
	//
	// cell.PrimaryListener is referenced by BOTH the HTTP listener (:8083) and this
	// gRPC listener (:8084): they are two independent TCP listeners sharing one
	// listener ROLE (ref), not one socket serving both protocols. HTTP and gRPC
	// listener refs live in separate bootstrap namespaces, so the shared ref is not
	// a conflict.
	//
	// Address + TLS/mTLS are resolved from the environment by newGRPCServerFromEnv
	// (see grpc.go): the demo runs plaintext out of the box (the adapter logs a
	// startup Warn for a non-loopback plaintext bind), durable mode must set TLS
	// or an explicit insecure opt-in. The interceptor chain mirrors the HTTP
	// primary listener's JWT auth: every RPC is authenticated (per-method public
	// is deferred to #1675). The metrics collector is backed by a Nop provider —
	// the iotdevice demo exports no metrics (HTTP path is Nop too). Cell
	// attribution + the grpc_ready readyz probe are wired below (PR-9 #1152, #1752):
	// interceptor.NewServerInterceptors mints the ONE shared ServiceRegistrar +
	// DrainSignal internally and adaptersgrpc.New binds them, so this composition
	// root cannot mint (or mismatch) a registrar/drain — asm.CellIDs() supplies the
	// metrics closed set.
	grpcCollector, err := rtmetrics.NewGRPCProviderCollector(kernelmetrics.NopProvider{}, rtmetrics.ProviderCollectorConfig{})
	if err != nil {
		return fmt.Errorf("build grpc metrics collector: %w", err)
	}
	// One Deps drives both chains and the adapter binding: NewServerInterceptors
	// (inside newGRPCServerFromEnv) mints the shared registrar/drain and wires the
	// unary + stream chains from them, so a composition root cannot accidentally
	// omit the stream chain or wire different instances. devicecell serves a
	// server-streaming RPC (WatchCommands), so the minted drain cancels in-flight
	// watches instead of holding the graceful-stop budget.
	grpcDeps := interceptor.Deps{
		Verifier:        jwtVerifier,
		Clock:           clk,
		Collector:       grpcCollector,
		CellIDClosedSet: asm.CellIDs(),
	}
	// Resolve the gRPC addr ONCE and bind it in both the adapter config and
	// WithGRPCListener below — bootstrap pre-binds the WithGRPCListener addr, so a
	// divergent adapter Config.Addr would be ignored (#1737 F2).
	grpcAddr := grpcAddrFromEnv()
	grpcServer, err := newGRPCServerFromEnv(durabilityMode, grpcAddr, grpcDeps)
	if err != nil {
		return fmt.Errorf("build grpc server: %w", err)
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		// PrimaryListener stays all-interfaces (:8083) so remote IoT devices can
		// reach it over the network. InternalListener is the cell→cell control
		// plane (/internal/v1/*) and binds loopback per docs/ops/listener-topology.md;
		// a real deployment sets a VPC-only addr + NetworkPolicy instead.
		bootstrap.WithListener(cell.PrimaryListener, ":8083", []auth.ListenerAuth{jwtPlan}),
		bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9083", internalAuthChain),
		// #673: a dedicated HealthListener is mandatory — /healthz, /readyz,
		// /metrics no longer fall back onto the primary listener.
		bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9093", []auth.ListenerAuth{auth.AuthNone{}}),
		bootstrap.WithGRPCListener(cell.PrimaryListener, grpcServer, grpcAddr),
		bootstrap.WithHealthRoutes(healthOpts...),
	}
	// MQTT channel options (health probe + managed closer) when enabled.
	opts = append(opts, mqttBootstrapOpts...)
	// Command-relay subsystem wiring (#1698): ConsumerBase is required because the
	// devicecell registers the event.device-registered.v1 subscription (phase6
	// fails fast otherwise); WithRelay drives the relay lifecycle + outbox polling.
	opts = append(
		opts,
		bootstrap.WithConsumerBase(crs.consumerBase),
	)
	// Durable mode: register the PG pool as a managed closer so framework
	// LIFO teardown closes it even when app.Run returns an error followed by
	// os.Exit(1). Defer-based cleanup would be skipped on that path.
	if pgPool != nil {
		opts = append(opts, bootstrap.WithManagedCloser(pgPool))
	}
	// Relay registered LAST → stopped FIRST (LIFO): the relay must stop before the
	// pool closes (durable) so no in-flight poll touches a closed pool.
	opts = append(opts, bootstrap.WithRelay(crs.relay))
	app := bootstrap.New(clk, opts...)

	logger.Info("iotdevice: starting on :8083; protected routes require an RS256 bearer token")
	return app.Run(ctx)
}

// commandRelaySubsystem bundles the wiring the async command-relay path needs:
// the writer-backed bootstrap emitter the devicebootstrap reactive slice emits
// into, the ConsumerBase that idempotency-guards the device-registered
// subscriber, the relay that polls the outbox store and dispatches command
// entries in-process, and the CellTxManager the bootstrap slice uses to wrap
// command.EmitAsync in a real transaction in durable mode (#1698).
type commandRelaySubsystem struct {
	bootstrapEmitter   outbox.CellEmitter
	bootstrapTxManager persistence.CellTxManager
	consumerBase       *outbox.ConsumerBase
	relay              *outboxruntime.Relay
}

// buildCommandRelaySubsystem wires the async command-relay subsystem for the
// resolved durability mode (mirrors examples/ssobff/app.go: NewOutboxStore +
// NewOutboxWriter + NewRelay + ConsumerBase). The relay polls store, publishes
// events to eb, and in-process-dispatches command.devicecommand.enqueue.v1
// entries to the registered handler — wrapped in the Claimer so an at-least-once
// device-registered redelivery cannot enqueue the same bootstrap command twice.
//
// In demo mode the store and writer are the SAME outboxtest.FakeStore (it
// implements both outbox.Store and kout.Writer). In durable mode they are an
// adapterpg.PGOutboxStore (relay poll) + adapterpg.OutboxWriter (producer write)
// over the shared pool — iotdevice durable already applies the platform outbox
// migration. Durable mode refuses a process-local command Claimer unless the
// operator explicitly acknowledges this iotdevice process is single-pod; a PG
// outbox store coordinates row leases across pods, but an in-memory Claimer does
// not coordinate command_id dedup across pods.
func buildCommandRelaySubsystem(
	clk clock.Clock,
	eb outbox.Publisher,
	commandReg *commandruntime.Registry,
	pool *adapterpg.Pool,
) (commandRelaySubsystem, error) {
	var (
		store              outboxruntime.Store
		writer             outbox.Writer
		bootstrapTxManager persistence.CellTxManager
	)
	if pool == nil {
		fakeStore := outboxtest.NewFakeStore()
		store = fakeStore
		writer = fakeStore // FakeStore satisfies both outbox.Store and kout.Writer.
		// Demo mode: DemoCellTxManager is a no-op that just calls the closure —
		// FakeStore.Write ignores the tx context so no real tx is needed.
		bootstrapTxManager = outbox.DemoCellTxManager()
	} else {
		store = adapterpg.NewOutboxStore(pool.DB(), clk)
		writer = adapterpg.NewOutboxWriter(clk)
		// Durable mode: wrap PG TxManager so command.EmitAsync gets a real tx in
		// ctx — adapterpg.OutboxWriter.Write calls persistence.TxFromContext[pgx.Tx]
		// and returns ErrAdapterPGNoTx without one.
		bootstrapTxManager = persistence.WrapForCell(adapterpg.NewTxManager(pool))
	}

	writerEmitter, err := outbox.NewWriterEmitter(writer)
	if err != nil {
		return commandRelaySubsystem{}, fmt.Errorf("bootstrap writer emitter: %w", err)
	}
	// Single shared Claimer feeds both the relay's command-dispatch path and the
	// ConsumerBase event-subscriber path.
	claimer, err := commandRelayClaimer(clk, pool != nil)
	if err != nil {
		return commandRelaySubsystem{}, err
	}

	consumerBase, err := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{}, clk)
	if err != nil {
		return commandRelaySubsystem{}, fmt.Errorf("consumer base: %w", err)
	}

	relay := outboxruntime.NewRelay(clk, store, eb, outboxruntime.DefaultRelayConfig())
	// First production WithCommandDispatch callsite: the map value MUST be the
	// generated cmdenqueue.DispatchAsync direct symbol (COMMAND-ASYNC-DISPATCH-CALLER-01).
	relay.WithCommandDispatch(commandReg, map[commandruntime.CommandID]commandruntime.AsyncDispatchFunc{
		cmdenqueue.DispatchID: cmdenqueue.DispatchAsync,
	}, claimer)

	return commandRelaySubsystem{
		bootstrapEmitter:   outbox.WrapEmitterForCell(writerEmitter),
		bootstrapTxManager: bootstrapTxManager,
		consumerBase:       consumerBase,
		relay:              relay,
	}, nil
}

func commandRelayClaimer(clk clock.Clock, durable bool) (idempotency.Claimer, error) {
	if durable && !envTrue(envDurableSinglePod) {
		return nil, fmt.Errorf(
			"iotdevice durable command relay requires a distributed idempotency claimer; "+
				"set %s=true only for an explicitly single-pod demo deployment",
			envDurableSinglePod,
		)
	}
	return idempotency.NewInMemClaimer(clk), nil
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
	migrator, err := adapterpg.NewMigrator(pool, migrationsFS, migration.PlatformNamespace)
	if err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("migrator: %w", err)
	}
	if err := migrator.Up(ctx); err != nil {
		closePool()
		return nil, nil, 0, nil, fmt.Errorf("migrate up: %w", err)
	}
	logger.Info("iotdevice: migrations applied")

	if err := adapterpg.VerifyExpectedVersion(ctx, pool, migrationsFS, migration.PlatformNamespace); err != nil {
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
			assemblyID, len(cellIDs), len(mods), hint,
		)
	}
	for i, want := range cellIDs {
		if got := mods[i].ID(); got != want {
			return fmt.Errorf(
				"%s: assembly.yaml cells[%d]=%q ↔ modules_gen.go=%q drift; %s",
				assemblyID, i, want, got, hint,
			)
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
