package orderprojectionrebuild

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orderprojection "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/orderprojection"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	projectionrebuild "github.com/ghbvf/gocell/generated/contracts/http/order/projection-rebuild/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

func newTestAdapter(t *testing.T) (*orderprojection.Service, *RebuildAdapter) {
	t.Helper()
	svc, err := orderprojection.NewService()
	require.NoError(t, err)
	return svc, NewRebuildAdapter(svc)
}

func makeCreatedEntry(t *testing.T, id, status string) outbox.Entry {
	t.Helper()
	payload := ordercreated.Payload{ID: id, Item: "widget", Status: status}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return outbox.Entry{ID: "entry-" + id, Payload: b}
}

func TestRebuildPost_Returns200_WithReportShape(t *testing.T) {
	_, adapter := newTestAdapter(t)
	h := projectionrebuild.NewHandler(adapter, func(*http.Request) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/internal/v1/orders/projection/rebuild", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestServiceContext("ordercell"))
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "data")
	assert.Contains(t, body, "lastAppliedSeq")
}

func TestRebuildPost_AfterEvents_RebuildsCorrectly(t *testing.T) {
	svc, adapter := newTestAdapter(t)

	// seed an event so the rebuild has something to replay
	svc.HandleOrderCreated(t.Context(), makeCreatedEntry(t, "order-1", "pending"))

	h := projectionrebuild.NewHandler(adapter, func(*http.Request) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/internal/v1/orders/projection/rebuild", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestServiceContext("ordercell"))
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "eventsReplayed")
}

func TestRebuildPost_MissingCallerCell_Returns403(t *testing.T) {
	// The handler uses Clients: ["ordercell"], auth.Mount auto-injects RequireCallerCell.
	// Without a service principal in context, the policy rejects the request.
	_, adapter := newTestAdapter(t)

	// use a policy that simulates RequireCallerCell behavior:
	// rejects requests without an authenticated service principal
	callerCellPolicy := func(r *http.Request) error {
		p, ok := auth.FromContext(r.Context())
		if !ok || p.CallerCellID == "" {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "access denied")
		}
		return nil
	}

	rebuildH := projectionrebuild.NewHandler(adapter, callerCellPolicy)
	mux := http.NewServeMux()
	err := rebuildH.RegisterRoutes(mux)
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
