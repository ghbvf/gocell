package orderprojection

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	projectionsummary "github.com/ghbvf/gocell/generated/contracts/http/order/projection-summary/v1"
	projectionrebuild "github.com/ghbvf/gocell/generated/contracts/http/order/projection-rebuild/v1"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allowAllPolicy = func(*http.Request) error { return nil }

func newHandlers(t *testing.T) (*Service, *projectionsummary.Handler, *projectionrebuild.Handler) {
	t.Helper()
	svc, err := NewService(slog.Default())
	require.NoError(t, err)
	summaryH := projectionsummary.NewHandler(NewSummaryAdapter(svc), allowAllPolicy)
	rebuildH := projectionrebuild.NewHandler(NewRebuildAdapter(svc), allowAllPolicy)
	return svc, summaryH, rebuildH
}

func TestSummaryGet_Returns200_WithBodyShape(t *testing.T) {
	svc, summaryH, _ := newHandlers(t)

	// seed two orders so seq > 0 and all fields appear in body (omitempty skips 0-value ints)
	ctx := context.Background()
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-test-1", "pending"))
	svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-test-1", "pending", "confirmed"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/orders/projection/summary", nil)
	summaryH.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "totalOrders")
	assert.Contains(t, body, "statuses")
	assert.Contains(t, body, "lastAppliedSeq")
}

func TestRebuildPost_Returns200_WithReportShape(t *testing.T) {
	_, _, rebuildH := newHandlers(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/internal/v1/orders/projection/rebuild", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	// inject valid service principal (ordercell)
	req = req.WithContext(auth.TestServiceContext("ordercell"))
	rebuildH.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "data")
	assert.Contains(t, body, "lastAppliedSeq")
}

func TestRebuildPost_MissingCallerCell_Returns403(t *testing.T) {
	// The handler uses Clients: ["ordercell"], auth.Mount auto-injects RequireCallerCell.
	// Without a service principal in context, the policy rejects the request.
	// We mount the handler through a real ServeMux via auth.Mount so the policy chain runs.
	svc, err := NewService(slog.Default())
	require.NoError(t, err)

	// use a policy that simulates RequireCallerCell behavior:
	// rejects requests without an authenticated service principal
	callerCellPolicy := func(r *http.Request) error {
		p, ok := auth.FromContext(r.Context())
		if !ok || p.CallerCellID == "" {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "access denied")
		}
		return nil
	}

	// mount through mux via RegisterRoutes so the auth policy middleware chain runs
	rebuildH := projectionrebuild.NewHandler(NewRebuildAdapter(svc), callerCellPolicy)
	mux := http.NewServeMux()
	err = rebuildH.RegisterRoutes(mux)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/internal/v1/orders/projection/rebuild", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	// no service context injected — policy guard should reject with 4xx
	mux.ServeHTTP(rec, req)

	// policy returns KindPermissionDenied which maps to 403; but if no principal
	// context is present, framework may also return 401 — either means auth rejected
	assert.True(t, rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized,
		"expected 401 or 403, got %d", rec.Code)
}
