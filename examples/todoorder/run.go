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

	"github.com/ghbvf/gocell/kernel/auth"

	ordercell "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/bootstrap"
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

// runTodoorder is the hand-written runtime helper for the todoorder assembly.
// It is called by the generated main.go and owns environment loading +
// bootstrap wiring.
func runTodoorder(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	mods, err := runTodoorderModules(assemblyID, assemblyCellIDs)
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

	// Cursor codec for pagination (demo mode).
	cursorCodec, err := query.NewCursorCodec([]byte("todoorder-cursor-key-32bytes!!!!"))
	if err != nil {
		return fmt.Errorf("create cursor codec: %w", err)
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
		return fmt.Errorf("register ordercell: %w", err)
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

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ":8082",
			[]auth.ListenerAuth{jwtPlan}),
		// demo loopback 隔离，生产按容器网络拓扑配置
		bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9082", internalAuthChain),
		// #673: a dedicated HealthListener is mandatory — /healthz, /readyz,
		// /metrics no longer fall back onto the primary listener.
		bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9092", []auth.ListenerAuth{auth.AuthNone{}}),
		bootstrap.WithHealthRoutes(healthOpts...),
	)

	logger.Info("todoorder: starting on :8082; protected routes require an RS256 bearer token")
	return app.Run(ctx)
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
