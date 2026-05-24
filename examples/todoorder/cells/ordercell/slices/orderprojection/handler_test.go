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

var allowAllPolicy = func(*http.Request) error { return nil }

func newSummaryHandler(t *testing.T) (*Service, *projectionsummary.Handler) {
	t.Helper()
	svc, err := NewService()
	require.NoError(t, err)
	summaryH := projectionsummary.NewHandler(NewSummaryAdapter(svc), allowAllPolicy)
	return svc, summaryH
}

func TestSummaryGet_Returns200_WithBodyShape(t *testing.T) {
	svc, summaryH := newSummaryHandler(t)

	// seed two orders so seq > 0 and all fields appear in body (omitempty skips 0-value ints)
	ctx := context.Background()
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-test-1", "pending"))
	svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-test-1"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/orders/projection/summary", nil)
	summaryH.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "totalOrders")
	assert.Contains(t, body, "statuses")
	assert.Contains(t, body, "lastAppliedSeq")
}
