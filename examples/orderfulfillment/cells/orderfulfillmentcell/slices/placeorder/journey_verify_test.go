package placeorder_test

// journey_verify_test.go — non-integration journey auto-verify entry points.
//
// These wrapper tests satisfy VERIFY-06 checkRef resolution without the
// //go:build integration tag (gocell validate --strict runs
// ./examples/orderfulfillment/... untagged). Unlike a bare HTTP 202 check, each
// journey drives the REAL saga coordinator (all in-memory: MemJournal +
// DemoTxRunner + NoopEmitter) to a terminal state and then verifies the
// user-facing read closure via GET /api/v1/orders/{id}. A regression that leaves
// the saga unexecuted, mis-projects its status, or breaks the place→read round
// trip is caught here, not only under -tags=integration.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/sagaimpl"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	placeorder "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
	placeordergen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/placeorder/v1"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	saga "github.com/ghbvf/gocell/runtime/saga"
)

// journeyEnv holds the wired placeorder + orderstatus handlers sharing one
// MemJournal that a running coordinator drives forward.
type journeyEnv struct {
	placeorderH  http.Handler
	orderstatusH http.Handler
	ctx          context.Context
}

// newJourneyEnv builds the full in-memory coordinator wiring: shared
// OrderRepository + MemJournal, the four saga step stores, a started
// Coordinator, and the placeorder + orderstatus HTTP handlers. The coordinator
// is stopped on t.Cleanup.
func newJourneyEnv(t *testing.T) journeyEnv {
	t.Helper()

	clk := clock.Real()
	jrnl, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
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
		jrnl,
		koutbox.DemoTxRunner{},
		koutbox.NewNoopEmitter(),
		reg,
		clk,
		saga.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	ownerCtx, cancel := context.WithCancel(context.Background())
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

	posvc, err := placeorder.NewService(
		clk,
		placeorder.WithOrderRepository(orders),
		placeorder.WithJournal(jrnl),
	)
	if err != nil {
		t.Fatalf("placeorder.NewService: %v", err)
	}
	ossvc, err := orderstatus.NewService(
		clk,
		orderstatus.WithOrderRepository(orders),
		orderstatus.WithJournal(jrnl),
	)
	if err != nil {
		t.Fatalf("orderstatus.NewService: %v", err)
	}

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
// coordinator runs all forward steps, and GET /api/v1/orders/{id} reports
// "succeeded".
func TestJOrderfulfillmentHappyHappyPath(t *testing.T) {
	env := newJourneyEnv(t)
	orderID := env.postOrder(t, `{"item":"widget","amountCents":1299,"idempotencyKey":"jrny-happy-1"}`)
	env.waitStatus(t, orderID, orderstatus.StatusSucceeded)
}

// TestJOrderfulfillmentCompensateCompensateOnChargeFail is the journey auto-verify
// entry point for J-orderfulfillment-compensate
// (checkRef: journey.J-orderfulfillment-compensate.compensate-on-charge-fail).
// Drives the compensation closure: POST with paymentShouldFail=true enrolls the
// saga, chargePayment fails, the coordinator rolls back, and GET
// /api/v1/orders/{id} reports "compensated".
func TestJOrderfulfillmentCompensateCompensateOnChargeFail(t *testing.T) {
	env := newJourneyEnv(t)
	orderID := env.postOrder(t, `{"item":"widget","amountCents":1299,"paymentShouldFail":true,"idempotencyKey":"jrny-comp-1"}`)
	env.waitStatus(t, orderID, orderstatus.StatusCompensated)
}
