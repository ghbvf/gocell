package orderconfirm

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	confirmv1 "github.com/ghbvf/gocell/generated/contracts/http/order/confirm/v1"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/tests/contracttest"
)

var allowAllContractPolicy = func(*http.Request) error { return nil }

// newContractHandlerWithRepo creates a handler backed by the given repo.
func newContractHandlerWithRepo(t testing.TB, repo *mem.OrderRepository) (http.Handler, *recordingWriter) {
	t.Helper()
	writer := &recordingWriter{}
	svc, err := NewService(repo, slog.Default(),
		WithEmitter(mustEmitter(t, writer)),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
	)
	require.NoError(t, err)
	h := confirmv1.NewHandler(svc, allowAllContractPolicy)
	return h, writer
}

// TestHttpOrderConfirmV1Serve validates the HTTP contract for the confirm endpoint.
func TestHttpOrderConfirmV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.confirm.v1")

	// Schema validation: valid body
	c.ValidateRequest(t, []byte(`{"status":"confirmed"}`))
	// Schema validation: extra fields rejected
	c.MustRejectRequest(t, []byte(`{"status":"confirmed","extra":"bad"}`))

	// Seed an order for the handler call
	repo := mem.NewOrderRepository()
	order := seedOrderInRepo(t, repo, "ord-contract-test-001")
	h, _ := newContractHandlerWithRepo(t, repo)

	// PATCH /api/v1/orders/{id}/status — use chi-compatible path replacement
	path := strings.Replace(c.HTTP.Path, "{id}", order.ID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"status":"confirmed"}`))
	req.SetPathValue("id", order.ID)
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// seedOrderInRepo seeds a pending order directly into the repo for contract tests.
func seedOrderInRepo(t testing.TB, repo *mem.OrderRepository, id string) *struct{ ID string } {
	t.Helper()
	err := repo.Create(context.Background(), &domain.Order{ID: id, Item: "widget", Status: "pending"})
	require.NoError(t, err)
	return &struct{ ID string }{ID: id}
}

// TestEventOrderStatusChangedV1Publish validates the event contract after a confirm call.
func TestEventOrderStatusChangedV1Publish(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	httpContract := contracttest.LoadByID(t, root, "http.order.confirm.v1")
	eventContract := contracttest.LoadByID(t, root, "event.order-status-changed.v1")

	repo := mem.NewOrderRepository()
	orderID := "ord-event-contract-test-001"
	require.NoError(t, repo.Create(context.Background(), &domain.Order{ID: orderID, Item: "widget", Status: "pending"}))

	h, writer := newContractHandlerWithRepo(t, repo)

	path := strings.Replace(httpContract.HTTP.Path, "{id}", orderID, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(httpContract.HTTP.Method, path, strings.NewReader(`{"status":"confirmed"}`))
	req.SetPathValue("id", orderID)
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	httpContract.ValidateHTTPResponseRecorder(t, rec)

	require.Len(t, writer.entries, 1, "expected exactly one emitted outbox entry")
	entry := writer.entries[0]

	// Validate event payload against schema
	eventContract.ValidatePayload(t, entry.Payload)
	// Validate headers (eventId must be present)
	eventContract.ValidateHeaders(t, []byte(`{"eventId":"`+entry.ID+`"}`))
	// MustRejectPayload: missing required fields
	eventContract.MustRejectPayload(t, []byte(`{"id":"o-1"}`))
	// MustRejectHeaders: missing eventId
	eventContract.MustRejectHeaders(t, []byte(`{}`))

	// Check payload has correct IDs
	var payload struct {
		ID        string `json:"id"`
		OldStatus string `json:"oldStatus"`
		NewStatus string `json:"newStatus"`
	}
	require.NoError(t, json.Unmarshal(entry.Payload, &payload))
	require.Equal(t, orderID, payload.ID)
	require.Equal(t, "pending", payload.OldStatus)
	require.Equal(t, "confirmed", payload.NewStatus)
}
