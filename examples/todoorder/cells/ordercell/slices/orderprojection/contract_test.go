package orderprojection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	projectionsummary "github.com/ghbvf/gocell/generated/contracts/http/order/projection-summary/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// TestHttpOrderProjectionSummaryV1Serve verifies http.order.projection-summary.v1 provide contract.
func TestHttpOrderProjectionSummaryV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.projection-summary.v1")

	svc, err := NewService()
	require.NoError(t, err)
	h := projectionsummary.NewHandler(NewSummaryAdapter(svc), func(*http.Request) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	h.ServeHTTP(rec, req)

	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestProjectionOrderStatusSummaryV1Provide verifies that Query output matches the
// projection.order.status-summary.v1 payload schema.
func TestProjectionOrderStatusSummaryV1Provide(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "projection.order.status-summary.v1")

	ctx := context.Background()
	svc, err := NewService()
	require.NoError(t, err)

	// seed some events so the summary is non-trivial
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "c-order-1", "pending"))
	svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "c-order-1"))

	summary := svc.Query(ctx)

	statuses := make([]map[string]any, 0, len(summary.Statuses))
	for _, b := range summary.Statuses {
		statuses = append(statuses, map[string]any{
			"status":   b.Status,
			"count":    b.Count,
			"orderIds": b.OrderIDs,
		})
	}
	payload := map[string]any{
		"statuses":       statuses,
		"totalOrders":    summary.TotalOrders,
		"lastAppliedSeq": summary.LastAppliedSeq,
	}
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)

	c.ValidatePayload(t, payloadBytes)
}

// TestEventOrderCreatedV1Subscribe verifies subscribe contract for event.order-created.v1:
// HandleOrderCreated correctly processes conforming payloads and Acks them.
func TestEventOrderCreatedV1Subscribe(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "event.order-created.v1")

	ctx := context.Background()
	svc, err := NewService()
	require.NoError(t, err)

	// positive: valid payload must Ack
	validPayload := []byte(`{"id":"order-ct-1","item":"widget","status":"pending"}`)
	c.ValidatePayload(t, validPayload)

	result := svc.HandleOrderCreated(ctx, outboxtest.NewEntry("event.order-created.v1", validPayload))
	assert.Equal(t, outbox.Ack(), result, "HandleOrderCreated must Ack valid payload")

	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)

	// negative: payload missing required field must be rejected by schema
	c.MustRejectPayload(t, []byte(`{"item":"widget","status":"pending"}`))
}

// TestEventOrderStatusChangedV1Subscribe verifies subscribe contract for event.order-status-changed.v1:
// HandleOrderStatusChanged correctly processes conforming payloads and Acks them.
func TestEventOrderStatusChangedV1Subscribe(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "event.order-status-changed.v1")

	ctx := context.Background()
	svc, err := NewService()
	require.NoError(t, err)

	// seed a created order first
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-ct-2", "pending"))

	// positive: valid payload must Ack
	validPayload := []byte(`{"id":"order-ct-2","oldStatus":"pending","newStatus":"confirmed"}`)
	c.ValidatePayload(t, validPayload)

	result := svc.HandleOrderStatusChanged(ctx, outboxtest.NewEntry("event.order-status-changed.v1", validPayload))
	assert.Equal(t, outbox.Ack(), result, "HandleOrderStatusChanged must Ack valid payload")

	// negative: payload missing required field must be rejected by schema
	c.MustRejectPayload(t, []byte(`{"id":"order-ct-2","oldStatus":"pending"}`))
}
