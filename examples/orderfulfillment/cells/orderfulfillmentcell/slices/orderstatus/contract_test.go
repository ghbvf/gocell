package orderstatus_test

// contract_test.go — contract test for http.orderfulfillment.orderstatus.v1.
//
// Test name TestHttpOrderfulfillmentOrderstatusV1Serve is derived by the
// verify runner from slice.yaml verify.contract entry:
//   contract.http.orderfulfillment.orderstatus.v1.serve
//   → fullPath = "http.orderfulfillment.orderstatus.v1.serve"
//   → each segment camelized → "HttpOrderfulfillmentOrderstatusV1Serve"
//   → prefix "Test" → TestHttpOrderfulfillmentOrderstatusV1Serve

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/tests/contracttest"
)

func newContractSetup(t testing.TB) (http.Handler, *mem.OrderRepository, *journal.MemJournal) {
	t.Helper()
	clk := clock.Real()
	repo := mem.NewOrderRepository()
	jrnl, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}
	svc, err := orderstatus.NewService(
		clk,
		orderstatus.WithOrderRepository(repo),
		orderstatus.WithJournal(jrnl),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := orderstatusgen.NewHandler(orderstatus.NewHandler(svc))
	return h, repo, jrnl
}

// TestHttpOrderfulfillmentOrderstatusV1Serve verifies the orderstatus contract:
// - known order with enrolled saga returns 200 + schema-valid response
// - unknown order returns 404 + error envelope
func TestHttpOrderfulfillmentOrderstatusV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "orderfulfillment")
	c := contracttest.LoadByID(t, root, "http.orderfulfillment.orderstatus.v1")
	h, repo, jrnl := newContractSetup(t)

	orderID := "ord-test-contract-01"
	order := &domain.Order{ID: orderID, Item: "widget", AmountCents: 1000, CreatedAt: time.Now()}
	if err := repo.Create(t.Context(), order); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Enqueue the saga instance so Load returns an empty slice (accepted state)
	// rather than KindNotFound (orphan order error introduced by F1).
	defIDStr, err := idutil.NewUUID()
	if err != nil {
		t.Fatalf("NewUUID: %v", err)
	}
	inst := ksaga.NewInstance(idutil.SafeID(orderID), idutil.SafeID(defIDStr), time.Now())
	if err := jrnl.Enqueue(t.Context(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	path := fmt.Sprintf("/api/v1/orders/%s", orderID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	// SetPathValue injects the {id} path parameter into the stdlib request so
	// the generated handler can read it via r.PathValue("id") without chi routing.
	req.SetPathValue("id", orderID)
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpOrderfulfillmentOrderstatusV1Serve_NotFound(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "orderfulfillment")
	c := contracttest.LoadByID(t, root, "http.orderfulfillment.orderstatus.v1")
	h, _, _ := newContractSetup(t)

	missingID := "ord-does-not-exist"
	path := fmt.Sprintf("/api/v1/orders/%s", missingID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req.SetPathValue("id", missingID)
	h.ServeHTTP(rec, req)

	errcodetest.AssertWireCode(t, rec, http.StatusNotFound, errcode.ErrOrderNotFound)
}
