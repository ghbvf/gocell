package orderprojection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	projectionsummary "github.com/ghbvf/gocell/generated/contracts/http/order/projection-summary/v1"
)

func newSummaryHandler(t *testing.T) (*Service, *projectionsummary.Handler) {
	t.Helper()
	svc, err := NewService()
	require.NoError(t, err)
	summaryH := projectionsummary.NewHandler(NewSummaryAdapter(svc), testResolver())
	return svc, summaryH
}

func TestSummaryGet_Returns200_WithBodyShape(t *testing.T) {
	svc, summaryH := newSummaryHandler(t)

	// seed two orders to ensure non-empty response
	ctx := context.Background()
	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-test-1", "pending")))
	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-test-2", "confirmed")))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/orders/projection/summary", nil)
	summaryH.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "totalOrders")
	assert.Contains(t, body, "statuses")
	// lastAppliedSeq is no longer in the response — the harness owns the offset
	assert.NotContains(t, body, "lastAppliedSeq")
}
