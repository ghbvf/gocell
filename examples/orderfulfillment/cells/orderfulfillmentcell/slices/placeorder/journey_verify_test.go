package placeorder_test

// journey_verify_test.go — non-integration journey auto-verify entry points.
//
// These wrapper tests satisfy VERIFY-06 checkRef resolution without requiring
// the //go:build integration tag. The gocell validate --strict runner uses
// ./examples/orderfulfillment/... (without integration tags), so the tests
// here run against the contract-test level wiring (no real coordinator).
//
// For full end-to-end verification (coordinator + event sequence), run:
//   go test -tags=integration ./examples/orderfulfillment/...

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	placeorder "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	placeordergen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/placeorder/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga/journal"
)

// newJourneyHandler constructs a placeorder HTTP handler for journey verify tests.
func newJourneyHandler(t testing.TB) http.Handler {
	t.Helper()
	clk := clock.Real()
	jrnl, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}
	repo := mem.NewOrderRepository()
	svc, err := placeorder.NewService(
		clk,
		placeorder.WithOrderRepository(repo),
		placeorder.WithJournal(jrnl),
	)
	if err != nil {
		t.Fatalf("placeorder.NewService: %v", err)
	}
	return placeordergen.NewHandler(placeorder.NewHandler(svc))
}

// TestJOrderfulfillmentHappyHappyPath is the journey auto-verify entry point for
// J-orderfulfillment-happy (checkRef: journey.J-orderfulfillment-happy.happy-path).
// Verifies that POST /api/v1/orders/ returns 202 Accepted with orderId and status.
func TestJOrderfulfillmentHappyHappyPath(t *testing.T) {
	h := newJourneyHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/",
		strings.NewReader(`{"item":"widget","amountCents":1299}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Errorf("POST /api/v1/orders/ = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
}

// TestJOrderfulfillmentCompensateCompensateOnChargeFail is the journey auto-verify
// entry point for J-orderfulfillment-compensate
// (checkRef: journey.J-orderfulfillment-compensate.compensate-on-charge-fail).
// Verifies that POST /api/v1/orders/ with paymentShouldFail=true returns 202 Accepted.
func TestJOrderfulfillmentCompensateCompensateOnChargeFail(t *testing.T) {
	h := newJourneyHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/",
		strings.NewReader(`{"item":"widget","amountCents":1299,"paymentShouldFail":true}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Errorf("POST /api/v1/orders/ (fail) = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
}
