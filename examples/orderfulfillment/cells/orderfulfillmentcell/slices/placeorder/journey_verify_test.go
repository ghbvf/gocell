package placeorder_test

// journey_verify_test.go — non-integration journey auto-verify entry points.
//
// These wrapper tests satisfy VERIFY-06 checkRef resolution without the
// //go:build integration tag (gocell validate --strict runs
// ./examples/orderfulfillment/... untagged). Unlike a bare HTTP 202 check, each
// journey drives the REAL saga coordinator (all in-memory: MemJournal +
// DemoTxRunner + NoopEmitter) to a terminal state and then verifies the
// user-facing read closure via GET /api/v1/orders/{id}.
//
// Architecture: the orderstatus slice now uses a CQRS projection read model
// populated by a saga-journal Tailer, not a request-time journal scan. The
// test wires the full chain: Coordinator drives the saga to a terminal state,
// the Tailer drains journal events and applies them to a MemReadModel, then
// waitStatus polls GetOrderStatus which reads from the read model.
//
// The polling approach (waitStatus / testwait.External) handles the eventual
// consistency gap between the coordinator writing terminal events and the
// Tailer applying them to the read model.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cellmodules/sagaprojectiondeps"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/projection"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/sagaimpl"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	placeorder "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
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

// tailerConfig is the per-test Tailer poll config: fast polling for unit-test
// speed, minimal lease TTL (matches distlock.MinTTL = 1ms, but we use 2ms to
// keep above minimum after any rounding).
const tailerLeaseTTL = 2 * time.Millisecond

// journeyEnv holds the wired placeorder + orderstatus handlers sharing one
// MemJournal that a running coordinator drives forward. The Tailer drains
// journal events into the MemReadModel, making GetOrderStatus eventually
// consistent with the coordinator's terminal state.
type journeyEnv struct {
	placeorderH  http.Handler
	orderstatusH http.Handler
	ctx          context.Context
}

// newJourneyEnv builds the full in-memory coordinator + saga-journal Tailer
// wiring: shared OrderRepository + MemJournal + MemReadModel + the four saga
// step stores, a started Coordinator, and a started Tailer. Both are stopped on
// t.Cleanup.
func newJourneyEnv(t *testing.T) journeyEnv {
	t.Helper()

	clk := clock.Real()
	ctx := context.Background()

	// Resolve saga-projection deps from the demo/memory topology (MemJournal,
	// MemOwnerCheckpointStore, in-process locker, DemoTxRunner).
	topo, err := bootstrap.NewTopology("", "memory", false)
	if err != nil {
		t.Fatalf("NewTopology: %v", err)
	}
	deps, err := sagaprojectiondeps.Resolve(ctx, clk, topo, sagaprojectiondeps.Config{})
	if err != nil {
		t.Fatalf("sagaprojectiondeps.Resolve: %v", err)
	}

	orders := mem.NewOrderRepository()
	impl, err := sagaimpl.NewImpl(
		orders,
		mem.NewInventoryStore(map[string]int{"widget": 100}),
		mem.NewPaymentStore(),
		mem.NewShipmentStore(),
	)
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	reg, err := of.Register(impl)
	if err != nil {
		t.Fatalf("of.Register: %v", err)
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
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	ownerCtx, cancel := context.WithCancel(ctx)
	go func() {
		if startErr := coord.Start(ownerCtx); startErr != nil && ownerCtx.Err() == nil {
			t.Logf("coord.Start returned: %v", startErr)
		}
	}()
	<-coord.Ready()
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.SelectShutdown)
		defer stopCancel()
		_ = coord.Stop(stopCtx)
		cancel()
	})

	// Build the CQRS read model for order-status.
	rm := projection.NewMemReadModel()

	// Build the placeorder service (uses the shared journal for Enqueue).
	posvc, err := placeorder.NewService(
		clk,
		placeorder.WithOrderRepository(orders),
		placeorder.WithJournal(deps.Journal),
	)
	if err != nil {
		t.Fatalf("placeorder.NewService: %v", err)
	}

	// Build the orderstatus service (uses the CQRS read model).
	ossvc, err := orderstatus.NewService(
		clk,
		orderstatus.WithOrderRepository(orders),
		orderstatus.WithOrderStatusReadModel(rm),
	)
	if err != nil {
		t.Fatalf("orderstatus.NewService: %v", err)
	}

	// Build the saga-journal source (implements both ReplaySource and Cursor).
	src, err := sagaprojection.NewSagaJournalSource(deps.Reader)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

	// Build the Tailer that drains journal events and calls HandleOrderEvent.
	// Use fast poll + minimal lease TTL for test speed.
	tailerCfg := tailer.DefaultConfig()
	tailerCfg.PollInterval = testtime.D20ms
	tailerCfg.LeaseTTL = tailerLeaseTTL

	tl, err := tailer.NewTailer(
		clk,
		src,                    // ReplaySource
		src,                    // Cursor (same instance)
		deps.OwnerStore,        // OwnerCheckpointStore
		deps.DeadLetters,       // DeadLetterStore (poison-event sink)
		deps.TxRunner,          // TxRunner
		ossvc.HandleOrderEvent, // Apply
		deps.Locker,            // Locker
		"orderfulfillmentcell",
		"order_saga_status",
		tailer.WithConfig(tailerCfg),
	)
	if err != nil {
		t.Fatalf("tailer.NewTailer: %v", err)
	}

	// Start the Tailer in a goroutine.
	go func() {
		if startErr := tl.Start(ownerCtx); startErr != nil && ownerCtx.Err() == nil {
			t.Logf("tailer.Start returned: %v", startErr)
		}
	}()
	// Wait for the Tailer's first tick (Ready) before accepting orders.
	select {
	case <-tl.Ready():
	case <-time.After(testtime.EventuallyDefault):
		t.Fatal("tl.Ready() timed out")
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.SelectShutdown)
		defer stopCancel()
		_ = tl.Stop(stopCtx)
	})

	return journeyEnv{
		placeorderH:  placeordergen.NewHandler(placeorder.NewHandler(posvc)),
		orderstatusH: orderstatusgen.NewHandler(orderstatus.NewHandler(ossvc)),
		ctx:          ownerCtx,
	}
}

// postOrder POSTs body to the placeorder handler, asserts 202, and returns the
// orderId from the response envelope.
func (e journeyEnv) postOrder(t *testing.T, body string) string {
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
			Status  string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode placeorder response: %v; body: %s", err, rec.Body.String())
	}
	if resp.Data.OrderID == "" {
		t.Fatalf("placeorder response missing orderId; body: %s", rec.Body.String())
	}
	return resp.Data.OrderID
}

// getStatus issues GET /api/v1/orders/{id} and returns (statusCode, status).
// On a non-200 the status string is empty.
func (e journeyEnv) getStatus(t *testing.T, orderID string) (int, string) {
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
		t.Fatalf("decode orderstatus response: %v; body: %s", err, rec.Body.String())
	}
	return rec.Code, resp.Data.Status
}

// waitStatus polls GET orderstatus until it reports want, failing on timeout.
// Uses testwait.External to satisfy TEST-SLEEP-DISCIPLINE-01 / TEST-TIME-LITERAL-01.
// The eventual consistency gap is between the coordinator writing terminal
// events to the journal and the Tailer draining them into the read model.
func (e journeyEnv) waitStatus(t *testing.T, orderID, want string) {
	t.Helper()
	var last string
	testwait.External(t, "orderstatus-poll", func() bool {
		if e.ctx.Err() != nil {
			return true // abort on context cancellation
		}
		code, st := e.getStatus(t, orderID)
		if code != http.StatusOK {
			return false
		}
		last = st
		return st == want
	}, testtime.EventuallyExtraLong, testtime.FastPoll)
	if last != want {
		t.Errorf("orderstatus for %s = %q, want %q", orderID, last, want)
	}
}

// TestJOrderfulfillmentHappyHappyPath is the journey auto-verify entry point for
// J-orderfulfillment-happy (checkRef: journey.J-orderfulfillment-happy.happy-path).
// Drives the full user closure: POST /api/v1/orders/ enrolls the saga, the
// coordinator runs all forward steps, the Tailer applies KindSagaSucceeded to the
// read model, and GET /api/v1/orders/{id} reports "succeeded".
func TestJOrderfulfillmentHappyHappyPath(t *testing.T) {
	env := newJourneyEnv(t)
	orderID := env.postOrder(t, `{"item":"widget","amountCents":1299,"idempotencyKey":"jrny-happy-1"}`)
	env.waitStatus(t, orderID, string(orderstatusgen.ResponseDataStatusSucceeded))
}

// TestJOrderfulfillmentCompensateCompensateOnChargeFail is the journey auto-verify
// entry point for J-orderfulfillment-compensate
// (checkRef: journey.J-orderfulfillment-compensate.compensate-on-charge-fail).
// Drives the compensation closure: POST with paymentShouldFail=true enrolls the
// saga, chargePayment fails, the coordinator rolls back, the Tailer applies
// KindSagaCompensated to the read model, and GET /api/v1/orders/{id} reports
// "compensated".
func TestJOrderfulfillmentCompensateCompensateOnChargeFail(t *testing.T) {
	env := newJourneyEnv(t)
	orderID := env.postOrder(t, `{"item":"widget","amountCents":1299,"paymentShouldFail":true,"idempotencyKey":"jrny-comp-1"}`)
	env.waitStatus(t, orderID, string(orderstatusgen.ResponseDataStatusCompensated))
}
