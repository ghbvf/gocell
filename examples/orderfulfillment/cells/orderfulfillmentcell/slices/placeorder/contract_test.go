package placeorder_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	placeorder "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	placeordergen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/placeorder/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/tests/contracttest"
)

func newContractHandler(t testing.TB) http.Handler {
	t.Helper()
	clk := clock.Real()
	jrnl, err := journal.NewMemJournal(clk)
	require.NoError(t, err)
	repo := mem.NewOrderRepository()
	svc, err := placeorder.NewService(
		clk,
		placeorder.WithOrderRepository(repo),
		placeorder.WithJournal(jrnl),
	)
	require.NoError(t, err)
	h := placeordergen.NewHandler(placeorder.NewHandler(svc))
	return h
}

func TestHttpOrderfulfillmentPlaceorderV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "orderfulfillment")
	c := contracttest.LoadByID(t, root, "http.orderfulfillment.placeorder.v1")
	h := newContractHandler(t)

	c.ValidateRequest(t, []byte(`{"item":"widget","amountCents":1000,"idempotencyKey":"ct-happy-1"}`))
	c.MustRejectRequest(t, []byte(`{"item":"widget","amountCents":1000,"idempotencyKey":"ct-happy-1","unknown":"bad"}`))
	// idempotencyKey is mapped to the order ID ("ord-"+key) and queried back as a
	// single path segment via GET /api/v1/orders/{id}; a '/' would split the segment
	// and break the round-trip, so the schema pattern must reject it.
	c.MustRejectRequest(t, []byte(`{"item":"widget","amountCents":1000,"idempotencyKey":"ct/with/slash"}`))

	rec := httptest.NewRecorder()
	body := `{"item":"widget","amountCents":1000,"idempotencyKey":"ct-happy-1"}`
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	require.Equal(t, 202, rec.Code)
}

func TestHttpOrderfulfillmentPlaceorderV1Serve_WithPaymentShouldFail(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "orderfulfillment")
	c := contracttest.LoadByID(t, root, "http.orderfulfillment.placeorder.v1")
	h := newContractHandler(t)

	rec := httptest.NewRecorder()
	body := `{"item":"gadget","amountCents":500,"paymentShouldFail":true,"idempotencyKey":"ct-payfail-1"}`
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	require.Equal(t, 202, rec.Code)
}
