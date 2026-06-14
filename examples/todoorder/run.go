// run.go is the hand-written runtime half behind the generated assembly
// entrypoint for todoorder. The generated main.go owns the assembly ID and
// cell order; this file owns environment loading and runtime option wiring.
//
// Demo mode injects NoopWriter + NoopTxRunner for a unified code path.
// Events are validated but discarded (no real broker). Production mode
// would inject a real outbox.Writer + persistence.TxRunner instead.
//
// Usage:
//
//	go run ./examples/todoorder
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/ghbvf/gocell/framework/kernel/auth"

	ordercell "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell"
	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// demoTxRunner is a pass-through TxRunner for demo mode: executes fn directly
// without a database transaction (no L2 atomicity). Production assemblies must
// inject a real TxRunner (e.g., postgres.TxManager).
type demoTxRunner struct{}

func (demoTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	ctx, drainAfterCommit := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(ctx)
	if err := fn(ctx); err != nil {
		persistence.TruncateAfterCommitTo(ctx, mark) // discard this scope's hooks
		return err
	}
	if drainAfterCommit {
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

// listenerAddrs carries the four bootstrap listener bind addresses. Production
// uses the fixed demo ports (defaultTodoorderListenerAddrs); the in-process
// startup smoke (run_smoke_test.go) overrides them with ephemeral loopback
// (127.0.0.1:0) so it can boot the real wiring through phase6 without binding
// fixed ports.
type listenerAddrs struct {
	primary  string
	internal string
	health   string
	admin    string
}

// defaultTodoorderListenerAddrs returns the fixed demo listener ports used by
// the generated main.go entrypoint (go run ./examples/todoorder).
func defaultTodoorderListenerAddrs() listenerAddrs {
	return listenerAddrs{
		primary:  ":8082",
		internal: "127.0.0.1:9082",
		health:   "127.0.0.1:9092",
		admin:    "127.0.0.1:9093",
	}
}

// runTodoorder is the hand-written runtime helper for the todoorder assembly.
// It is called by the generated main.go and owns environment loading +
// bootstrap wiring.
func runTodoorder(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	addrs := defaultTodoorderListenerAddrs()
	app, err := buildTodoorderBootstrap(assemblyID, assemblyCellIDs, addrs)
	if err != nil {
		return err
	}
	slog.Default().Info("todoorder: starting; protected routes require an RS256 bearer token",
		slog.String("primary_addr", addrs.primary))
	return app.Run(ctx)
}

// buildTodoorderBootstrap assembles the todoorder bootstrap from in-memory demo
// dependencies and the given listener addresses, returning the configured
// *bootstrap.Bootstrap without starting it. Splitting assembly from Run lets the
// startup smoke (run_smoke_test.go) boot the real wiring through phase6 (the
// projection coordinator — the exact stage PR #1483's missing WithConsumerBase
// crashed) on ephemeral ports. Production behavior is unchanged: runTodoorder
// passes defaultTodoorderListenerAddrs().
func buildTodoorderBootstrap(assemblyID string, assemblyCellIDs []string, addrs listenerAddrs) (*bootstrap.Bootstrap, error) {
	// The redacting slog default is sealed by the generated main.go's run()
	// (SLOG-HANDLER-SEALED-FUNNEL-01 A3 generated segment) before this function
	// runs, so logger (and every slog.Default() call) is already scrubbed.
	logger := slog.Default()

	// runTodoorderModules returns stub modules used solely for the assembly drift
	// check; the real ordercell is constructed directly below (this example does
	// not use the compositionAPI/CellModule Provide pattern).
	if _, err := runTodoorderModules(assemblyID, assemblyCellIDs); err != nil {
		return nil, err
	}

	internalAuthChain, err := newInternalAuthChainFromEnv()
	if err != nil {
		return nil, fmt.Errorf("configure internal listener auth: %w", err)
	}
	jwtVerifier, err := newJWTVerifierFromEnv()
	if err != nil {
		return nil, fmt.Errorf("configure JWT verifier: %w", err)
	}

	// Cursor codec for pagination (demo mode).
	cursorCodec, err := query.NewCursorCodec([]byte("todoorder-cursor-key-32bytes!!!!"))
	if err != nil {
		return nil, fmt.Errorf("create cursor codec: %w", err)
	}

	// Create the order cell with in-memory defaults.
	// Demo mode: NoopWriter + NoopTxRunner → unified outbox code path (zero fork).
	// Events are validated by NoopWriter then discarded. In production, inject
	// a real outbox.Writer (e.g., postgres.OutboxWriter) + persistence.TxRunner
	// (e.g., postgres.TxManager) for durable event delivery via relay.
	oc := ordercell.NewOrderCell(
		ordercell.WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{})),
		ordercell.WithTxManager(persistence.WrapForCell(demoTxRunner{})),
		ordercell.WithCursorCodec(cursorCodec),
		ordercell.WithLogger(logger),
	)

	// Build assembly and register the cell.
	asm := assembly.New(clock.Real(), assembly.Config{ID: assemblyID, DurabilityMode: outbox.DurabilityDemo})
	if err := asm.Register(oc); err != nil {
		return nil, fmt.Errorf("register ordercell: %w", err)
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
		return nil, fmt.Errorf("invalid JWT auth plan: %w", err)
	}

	// Operator control-plane (AdminListener) — configured only when operator
	// credentials are present in the environment, so the demo still starts out
	// of the box (the projection rebuild endpoint then stays programmatic-only).
	operatorAuth, operatorEnabled, err := newOperatorAuthFromEnv()
	if err != nil {
		return nil, fmt.Errorf("configure admin listener auth: %w", err)
	}

	// Demo wires in-memory projection infra; events are discarded by NoopWriter
	// so live consumption is best-effort (same as every todoorder demo) — the
	// harness still cold-starts and registers its readyz probe; faithful
	// PG-backed replay is tracked in backlog.
	// One MemProjectionEventSource is the canonical in-memory LiveCursor — wired as BOTH
	// the ReplaySource and the Cursor (matching the production PGProjectionEventSource
	// single-instance shape), so live carriers resolve and replay against the same store.
	projCheckpoint := projection.NewMemCheckpointStore()
	projSource := projection.NewMemProjectionEventSource()

	// Projections consume via the same ConsumerBase path as subscriptions, so
	// the projection coordinator (phase6) requires a ConsumerBase to be wired —
	// without it bootstrap fails fast at startup. Demo uses an in-memory
	// idempotency claimer (single-process only); production would inject a
	// distributed claimer (e.g. Redis).
	claimer := idempotency.NewInMemClaimer(clock.Real())
	consumerBase, err := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{}, clock.Real())
	if err != nil {
		return nil, fmt.Errorf("consumer base: %w", err)
	}

	// No WithMetricsProvider in demo → projection metric instruments are no-ops.
	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, addrs.primary,
			[]auth.ListenerAuth{jwtPlan}),
		// demo loopback 隔离，生产按容器网络拓扑配置
		bootstrap.WithListener(cell.InternalListener, addrs.internal, internalAuthChain),
		// #673: a dedicated HealthListener is mandatory — /healthz, /readyz,
		// /metrics no longer fall back onto the primary listener.
		bootstrap.WithListener(cell.HealthListener, addrs.health, []auth.ListenerAuth{auth.AuthNone{}}),
		bootstrap.WithHealthRoutes(healthOpts...),
		bootstrap.WithConsumerBase(consumerBase),
		bootstrap.WithProjectionCheckpointStore(projCheckpoint),
		bootstrap.WithProjectionTxRunner(demoTxRunner{}),
		bootstrap.WithProjectionReplaySource(projSource),
		bootstrap.WithProjectionCursor(projSource),
	}
	if operatorEnabled {
		// Operator control-plane on a network-isolated (loopback) admin port,
		// gated by operator credentials (defense in depth). The framework
		// projection rebuild endpoint
		// (POST /admin/v1/projection/{cell}/{name}/rebuild) is an operator→system
		// action — an ops tool / deployment pipeline presents the operator Basic
		// Auth credentials to rebuild ordercell's order_status projection. No
		// caller-cell allowlist (that was the prior /internal/v1/* cell→cell model).
		opts = append(opts,
			bootstrap.WithListener(cell.AdminListener, addrs.admin,
				[]auth.ListenerAuth{operatorAuth}),
			bootstrap.WithProjectionRebuildEndpoint(),
		)
	}

	return bootstrap.New(clock.Real(), opts...), nil
}

// runTodoorderModules validates that assembly.yaml cells (assemblyCellIDs)
// match the generated module list in modules_gen.go. A mismatch means
// `gocell generate assembly --id=todoorder` has not been re-run after an
// assembly.yaml change.
func runTodoorderModules(assemblyID string, cellIDs []string) ([]CellModule, error) {
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
// factory returns a value implementing ID(). The todoorder assembly uses
// stub module values only for drift detection; cell construction is done
// directly in runTodoorder.
type CellModule interface {
	ID() string
}

// OrderCellModule is the stub module for ordercell — returned by
// generatedCellModules() in modules_gen.go. It carries the cell ID for the
// assembly drift check and does not participate in Provide wiring (direct
// cell construction is used for this example assembly).
type OrderCellModule struct{}

// ID returns the ordercell identifier.
func (OrderCellModule) ID() string { return "ordercell" }
