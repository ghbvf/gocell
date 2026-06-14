//go:build integration

// Package placeorder_test: durable-replay e2e integration test for the
// saga-journal CQRS projection running on a PG backend.
//
// The scenario:
//
//  1. Get a fresh per-test PG pool from sharedPG (platform schema already present).
//  2. Apply the orderfulfillment migration (order_saga_status table) to the DB.
//  3. Build a postgres topology, resolve sagaprojectiondeps (PGJournal +
//     PGCheckpointStore + in-process locker + PGTxManager), build PGReadModel.
//  4. Wire a Coordinator on deps.Journal and run it to terminal state.
//  5. Phase 1 (Tailer A): start, drain journal events, confirm the PG read model
//     shows terminal status, capture checkpoint.
//  6. Phase 2 (restart): build a FRESH sagaprojectiondeps.Resolve against the SAME
//     pool (new in-process locker, new TxRunner, same PG backing), build a fresh
//     Tailer B, and assert:
//     a. PG read model still shows terminal status.
//     b. Checkpoint is preserved (the second tailer picks up from where Tailer A
//     stopped — does not regress).
//     c. Tailer B does not overwrite the status with an earlier value.
//
// Build constraint: this file requires `//go:build integration`. Run with
//
//	go test -tags integration ./examples/orderfulfillment/... -count=1 -timeout 300s
package placeorder_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/sagaprojectiondeps"
	ordercell "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/projection"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/sagaimpl"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	placeorder "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	ofmigrations "github.com/ghbvf/gocell/examples/orderfulfillment/migrations"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	koutbox "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/saga/sagaprojection"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	saga "github.com/ghbvf/gocell/framework/runtime/saga"
	"github.com/ghbvf/gocell/framework/runtime/saga/tailer"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
	placeordergen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/placeorder/v1"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
)

// applyOrderfulfillmentMigration applies the orderfulfillment example migration
// (creates the order_saga_status table) to pool. The platform schema (obtained
// from the pgtest template clone) is already present.
func applyOrderfulfillmentMigration(ctx context.Context, t *testing.T, pool *adapterpg.Pool) {
	t.Helper()
	migrator, err := adapterpg.NewMigrator(pool, ofmigrations.FS, ofmigrations.Namespace)
	if err != nil {
		t.Fatalf("durable_replay: new orderfulfillment migrator: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("durable_replay: run orderfulfillment migrations: %v", err)
	}
}

// buildPGDeps constructs a postgres topology + sagaprojectiondeps.Deps against pool.
// singlePod=true: in-process distlock (correct for a test that runs one replica).
func buildPGDeps(ctx context.Context, t *testing.T, pool *adapterpg.Pool) sagaprojectiondeps.Deps {
	t.Helper()
	clk := clock.Real()
	// adapterMode "real" is required for postgres storage (the topology coupling
	// validate enforces); single-pod true → in-process leader locker.
	topo, err := bootstrap.NewTopology("real", bootstrap.StorageBackendPostgres, true)
	if err != nil {
		t.Fatalf("durable_replay: create postgres topology: %v", err)
	}
	deps, err := sagaprojectiondeps.Resolve(ctx, clk, topo, sagaprojectiondeps.Config{Pool: pool})
	if err != nil {
		t.Fatalf("durable_replay: sagaprojectiondeps.Resolve: %v", err)
	}
	return deps
}

// buildCoordinatorForIntegration wires a Coordinator using deps.Journal +
// deps.TxRunner + NoopEmitter. In-memory saga step stores (orders, inventory,
// payment, shipment) are sufficient for this test — the journal and checkpoint
// are the PG-backed components under test.
func buildCoordinatorForIntegration(
	t *testing.T,
	deps sagaprojectiondeps.Deps,
	orders *mem.OrderRepository,
	inv *mem.InventoryStore,
) *saga.Coordinator {
	t.Helper()
	clk := clock.Real()
	pay := mem.NewPaymentStore()
	ship := mem.NewShipmentStore()
	impl, err := sagaimpl.NewImpl(orders, inv, pay, ship)
	if err != nil {
		t.Fatalf("durable_replay: NewImpl: %v", err)
	}
	reg, err := of.Register(impl)
	if err != nil {
		t.Fatalf("durable_replay: of.Register: %v", err)
	}
	cfg := saga.DefaultConfig()
	cfg.PollInterval = testtime.D20ms
	cfg.LeaseDuration = testtime.D2s
	cfg.HeartbeatInterval = testtime.D50ms

	coord, err := saga.NewCoordinator(
		deps.Journal,
		koutbox.DemoTxRunner{},
		koutbox.NewNoopEmitter(),
		reg,
		clk,
		saga.WithConfig(cfg),
		saga.WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("durable_replay: NewCoordinator: %v", err)
	}
	return coord
}

// startAndReadyCoordinator starts coord in a goroutine and waits for Ready.
// Returns a stop func (call at most once).
func startAndReadyCoordinator(t *testing.T, coord *saga.Coordinator) (ctx context.Context, stop func()) {
	t.Helper()
	ownerCtx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := coord.Start(ownerCtx); err != nil && ownerCtx.Err() == nil {
			t.Logf("durable_replay: coord.Start returned: %v", err)
		}
	}()
	<-coord.Ready()
	stop = func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.SelectShutdown)
		defer stopCancel()
		_ = coord.Stop(stopCtx)
		cancel()
	}
	return ownerCtx, stop
}

// buildTailer constructs a Tailer that applies journal events to apply via
// deps.OwnerStore / deps.TxRunner / deps.Locker.
func buildTailer(
	t *testing.T,
	deps sagaprojectiondeps.Deps,
	apply cellvocab.ProjectionApply,
) *tailer.Tailer {
	t.Helper()
	clk := clock.Real()
	src, err := sagaprojection.NewSagaJournalSource(deps.Reader)
	if err != nil {
		t.Fatalf("durable_replay: NewSagaJournalSource: %v", err)
	}
	cfg := tailer.DefaultConfig()
	cfg.PollInterval = testtime.D20ms
	cfg.LeaseTTL = tailerLeaseTTL

	tl, err := tailer.NewTailer(
		clk,
		src,
		src,
		deps.OwnerStore,
		deps.TxRunner,
		apply,
		deps.Locker,
		"orderfulfillmentcell",
		"order_saga_status",
		tailer.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("durable_replay: tailer.NewTailer: %v", err)
	}
	return tl
}

// startAndReadyTailer starts tl and waits for the first tick. Returns stop func.
func startAndReadyTailer(t *testing.T, tl *tailer.Tailer, ownerCtx context.Context) (stop func()) {
	t.Helper()
	go func() {
		if err := tl.Start(ownerCtx); err != nil && ownerCtx.Err() == nil {
			t.Logf("durable_replay: tailer.Start returned: %v", err)
		}
	}()
	select {
	case <-tl.Ready():
	case <-time.After(testtime.EventuallyDefault):
		t.Fatal("durable_replay: tl.Ready() timed out")
	}
	stop = func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.SelectShutdown)
		defer stopCancel()
		_ = tl.Stop(stopCtx)
	}
	return stop
}

// buildOrderstatusService builds an orderstatus.Service wired to the given read model.
func buildOrderstatusService(t *testing.T, orders *mem.OrderRepository, rm projection.OrderStatusReadModel) *orderstatus.Service {
	t.Helper()
	clk := clock.Real()
	svc, err := orderstatus.NewService(
		clk,
		orderstatus.WithOrderRepository(orders),
		orderstatus.WithOrderStatusReadModel(rm),
	)
	if err != nil {
		t.Fatalf("durable_replay: orderstatus.NewService: %v", err)
	}
	return svc
}

// pgEnv bundles the wired durable-replay environment for one phase.
type pgEnv struct {
	placeorderH  http.Handler
	orderstatusH http.Handler
	ctx          context.Context
}

// postOrder POSTs body to the placeorder handler, asserts 202, and returns the
// orderId from the response envelope.
func (e pgEnv) postOrder(t *testing.T, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	e.placeorderH.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/orders/ = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("durable_replay: decode placeorder response: %v; body: %s", err, rec.Body.String())
	}
	if resp.Data.OrderID == "" {
		t.Fatalf("durable_replay: placeorder response missing orderId; body: %s", rec.Body.String())
	}
	return resp.Data.OrderID
}

// getStatus issues GET /api/v1/orders/{id} and returns (statusCode, status).
func (e pgEnv) getStatus(t *testing.T, orderID string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+orderID, nil)
	req.SetPathValue("id", orderID)
	e.orderstatusH.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, ""
	}
	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("durable_replay: decode orderstatus response: %v; body: %s", err, rec.Body.String())
	}
	return rec.Code, resp.Data.Status
}

// waitStatus polls GET orderstatus until it equals want, failing on timeout.
func (e pgEnv) waitStatus(t *testing.T, orderID, want string) {
	t.Helper()
	var last string
	testwait.External(t, "orderstatus-pg-poll", func() bool {
		if e.ctx.Err() != nil {
			return true
		}
		code, st := e.getStatus(t, orderID)
		if code != http.StatusOK {
			return false
		}
		last = st
		return st == want
	}, testtime.EventuallyExtraLong, testtime.FastPoll)
	if last != want {
		t.Errorf("durable_replay: orderstatus for %s = %q, want %q (last seen)", orderID, last, want)
	}
}

// TestDurableReplay_PGProjectionSurvivesRestart is the e2e durable-replay
// integration test. It verifies that the saga-journal CQRS projection writes to
// PG and that a fresh tailer (simulated restart) picks up from the persisted
// checkpoint, without regressing the read model.
//
// The test is intentionally sequential (no t.Parallel) because it exercises the
// single shared per-test DB — parallelism within a test run is provided by
// running multiple independent tests, each with their own pool.
func TestDurableReplay_PGProjectionSurvivesRestart(t *testing.T) {
	ctx := context.Background()

	// ── Phase 0: Get a per-test PG pool, apply orderfulfillment migration ────
	pool := sharedPG.NewPerTestPool(t)
	applyOrderfulfillmentMigration(ctx, t, pool)

	// Shared in-memory stores (orders, inventory) — these are the saga step
	// stores. They live only for the test; the PG journal/checkpoint survive.
	orders := mem.NewOrderRepository()
	inv := mem.NewInventoryStore(map[string]int{"widget": 100})

	// ── Phase 1 setup: build PG deps + Coordinator + Tailer A + read model ──
	deps1 := buildPGDeps(ctx, t, pool)
	pgRM, err := ordercell.NewPGOrderStatusReadModel(pool)
	if err != nil {
		t.Fatalf("durable_replay: NewPGOrderStatusReadModel: %v", err)
	}

	coord1 := buildCoordinatorForIntegration(t, deps1, orders, inv)
	coordCtx1, stopCoord1 := startAndReadyCoordinator(t, coord1)
	defer stopCoord1() // belt-and-suspenders; normally stopped below

	ossvc1 := buildOrderstatusService(t, orders, pgRM)
	tailerCtx1, cancelTailer1 := context.WithCancel(coordCtx1)
	tl1 := buildTailer(t, deps1, ossvc1.HandleOrderEvent)
	stopTailer1 := startAndReadyTailer(t, tl1, tailerCtx1)

	// Build the HTTP handlers (shared by both phases via the same read model).
	clk := clock.Real()
	posvc, err := placeorder.NewService(
		clk,
		placeorder.WithOrderRepository(orders),
		placeorder.WithJournal(deps1.Journal),
		placeorder.WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("durable_replay: placeorder.NewService: %v", err)
	}

	env1 := pgEnv{
		placeorderH:  placeordergen.NewHandler(placeorder.NewHandler(posvc)),
		orderstatusH: orderstatusgen.NewHandler(orderstatus.NewHandler(ossvc1)),
		ctx:          coordCtx1,
	}

	// ── Phase 1 execution: place order, drive to terminal state, read via PG ──
	orderID := env1.postOrder(t, `{"item":"widget","amountCents":1299,"idempotencyKey":"pg-replay-1"}`)
	env1.waitStatus(t, orderID, string(orderstatusgen.ResponseDataStatusSucceeded))

	// Capture the status from the PG read model directly (not via orderstatus
	// service) to prove it was written to PG, not an in-memory cache.
	pgStatus, found, err := pgRM.Get(ctx, orderID)
	if err != nil {
		t.Fatalf("durable_replay: pgRM.Get (phase 1): %v", err)
	}
	if !found {
		t.Fatal("durable_replay: PG read model has no row for orderID after Tailer A drained journal")
	}
	if pgStatus != orderstatusgen.ResponseDataStatusSucceeded {
		t.Errorf("durable_replay: PG read model status = %q, want %q", pgStatus, orderstatusgen.ResponseDataStatusSucceeded)
	}

	// Capture the durable checkpoint offset that Tailer A persisted — must be > 0
	// to prove Tailer B can resume from a real position, not a cold-start 0.
	savedOffset, err := deps1.OwnerStore.LoadOffset(ctx, "orderfulfillmentcell", "order_saga_status")
	if err != nil {
		t.Fatalf("durable_replay: LoadOffset (phase 1 checkpoint capture): %v", err)
	}
	if savedOffset <= 0 {
		t.Errorf("durable_replay: checkpoint offset after Tailer A = %d, want > 0", savedOffset)
	}

	// ── Simulated restart: stop Tailer A + Coordinator ────────────────────────
	stopTailer1()
	cancelTailer1()
	stopCoord1()

	// ── Phase 2: fresh sagaprojectiondeps.Resolve against the SAME pool ──────
	// This simulates a process restart: new in-process locker, new TxRunner
	// instance, but same PG journal + checkpoint store underneath.
	deps2 := buildPGDeps(ctx, t, pool)

	// The PG read model is also constructed fresh (no in-memory cache).
	pgRM2, err := ordercell.NewPGOrderStatusReadModel(pool)
	if err != nil {
		t.Fatalf("durable_replay: NewPGOrderStatusReadModel (phase 2): %v", err)
	}

	ossvc2 := buildOrderstatusService(t, orders, pgRM2)
	tailerCtx2, cancelTailer2 := context.WithCancel(context.Background())
	defer cancelTailer2()
	tl2 := buildTailer(t, deps2, ossvc2.HandleOrderEvent)
	stopTailer2 := startAndReadyTailer(t, tl2, tailerCtx2)
	defer stopTailer2()

	env2 := pgEnv{
		orderstatusH: orderstatusgen.NewHandler(orderstatus.NewHandler(ossvc2)),
		ctx:          tailerCtx2,
	}

	// ── Phase 2 assertions ────────────────────────────────────────────────────
	// (a) PG read model still shows terminal status immediately (from PG row).
	pgStatus2, found2, err := pgRM2.Get(ctx, orderID)
	if err != nil {
		t.Fatalf("durable_replay: pgRM2.Get (phase 2): %v", err)
	}
	if !found2 {
		t.Fatal("durable_replay: PG read model row missing after restart — not durable")
	}
	if pgStatus2 != orderstatusgen.ResponseDataStatusSucceeded {
		t.Errorf("durable_replay: PG read model status after restart = %q, want %q",
			pgStatus2, orderstatusgen.ResponseDataStatusSucceeded)
	}

	// (b) The orderstatus service GET endpoint (which reads from pgRM2) still
	//     returns terminal status — no regression.
	env2.waitStatus(t, orderID, string(orderstatusgen.ResponseDataStatusSucceeded))

	// (c) Tailer B should not overwrite the status: poll a few ticks and confirm
	//     the status is stable (still "succeeded", not regressed to "running" or
	//     anything else).
	// We give Tailer B 3 × FastPoll ticks to re-process any journal events it
	// might replay, then verify the status is still terminal.
	time.Sleep(3 * testtime.FastPoll) //archtest:allow:test-sleep stability window: lets Tailer B tick a few times before asserting no-regression; no completion channel to wait on
	pgStatus3, found3, err := pgRM2.Get(ctx, orderID)
	if err != nil {
		t.Fatalf("durable_replay: pgRM2.Get (stability check): %v", err)
	}
	if !found3 {
		t.Fatal("durable_replay: PG read model row missing after stability sleep")
	}
	if pgStatus3 != orderstatusgen.ResponseDataStatusSucceeded {
		t.Errorf("durable_replay: status regressed after Tailer B: got %q, want %q",
			pgStatus3, orderstatusgen.ResponseDataStatusSucceeded)
	}

	// (d) Verify checkpoint is preserved: Tailer B must resume from the same
	//     offset Tailer A left, proving it reads the durable PG checkpoint and
	//     does NOT reset to 0 (which would let fold idempotency hide a full rescan).
	offset2, err := deps2.OwnerStore.LoadOffset(ctx, "orderfulfillmentcell", "order_saga_status")
	if err != nil {
		t.Fatalf("durable_replay: LoadOffset (phase 2 checkpoint verify): %v", err)
	}
	if offset2 != savedOffset {
		t.Errorf("durable_replay: checkpoint offset after Tailer B = %d, want %d (same as Tailer A — checkpoint preserved, not reset to 0)",
			offset2, savedOffset)
	}
}
