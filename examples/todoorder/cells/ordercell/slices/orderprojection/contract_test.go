package orderprojection

import (
	"context"
	"encoding/json"
	"errors"
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

// TestHttpOrderProjectionSummaryV1Serve verifies http.order.projection-summary.v1 serve contract.
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

	// seed an order so the summary is non-trivial
	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "c-order-1", "pending")))

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
		"statuses":    statuses,
		"totalOrders": summary.TotalOrders,
	}
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)

	c.ValidatePayload(t, payloadBytes)
}

// TestEventOrderCreatedV1Apply verifies the projection apply contract for
// event.order-created.v1: HandleOrderCreated correctly processes conforming
// payloads (returns nil) and rejects invalid ones (returns permanent error).
func TestEventOrderCreatedV1Apply(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "event.order-created.v1")

	ctx := context.Background()
	svc, err := NewService()
	require.NoError(t, err)

	// positive: valid payload must return nil (apply success)
	validPayload := []byte(`{"id":"order-ct-1","item":"widget","status":"pending"}`)
	c.ValidatePayload(t, validPayload)

	applyErr := svc.HandleOrderCreated(ctx, outboxtest.NewEntry("event.order-created.v1", validPayload))
	assert.NoError(t, applyErr, "HandleOrderCreated must return nil for valid payload")

	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)

	// negative: payload missing required field must be rejected by schema
	c.MustRejectPayload(t, []byte(`{"item":"widget","status":"pending"}`))

	// negative: invalid JSON must return permanent error
	badEntry := outboxtest.NewEntry("event.order-created.v1", []byte("not-json"))
	badErr := svc.HandleOrderCreated(ctx, badEntry)
	require.Error(t, badErr)
	var pe *outbox.PermanentError
	assert.True(t, errors.As(badErr, &pe), "undecodable payload must be a PermanentError")
}
