package ordercell

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dto "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/dto"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/http/router"
)

// initListenerRouter creates a fully-wired router from a real OrderCell so we
// can exercise the permission-gate + authorizer pipeline end-to-end. The cell
// uses an in-memory repo (demo mode) so all PIP lookups hit real data.
func initListenerRouter(t *testing.T) (*router.Router, *OrderCell) {
	t.Helper()
	return initCellWithRouter(t)
}

// createOrderAs sends POST /api/v1/orders/ as the given subject+roles and
// returns the created order ID.
func createOrderAs(t *testing.T, r *router.Router, c *OrderCell, subject string, roles []string, item string) string {
	t.Helper()
	body := `{"item":"` + item + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(body))
	req = req.WithContext(withAuthorizer(auth.TestContext(subject, roles), c))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, "createOrderAs: want 201")

	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.NotEmpty(t, resp.Data.ID, "createOrderAs: response must contain data.id")
	return resp.Data.ID
}

// TestListenerAuth_Owner_GetOrder verifies that the order creator (owner) can
// read their own order via GET /api/v1/orders/{id}.
func TestListenerAuth_Owner_GetOrder(t *testing.T) {
	r, c := initListenerRouter(t)
	const owner = "user-owner-a"

	orderID := createOrderAs(t, r, c, owner, []string{dto.RoleCustomer}, "owner-widget")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+orderID, nil)
	req = req.WithContext(withAuthorizer(auth.TestContext(owner, []string{dto.RoleCustomer}), c))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "order owner should get 200 on GET")
}

// TestListenerAuth_CrossOwner_GetOrder verifies that a different subject
// (non-owner) receives 403 when attempting to read another user's order.
func TestListenerAuth_CrossOwner_GetOrder(t *testing.T) {
	r, c := initListenerRouter(t)
	const owner = "user-owner-b"
	const intruder = "user-intruder-b"

	orderID := createOrderAs(t, r, c, owner, []string{dto.RoleCustomer}, "cross-owner-widget")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+orderID, nil)
	req = req.WithContext(withAuthorizer(auth.TestContext(intruder, []string{dto.RoleCustomer}), c))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "non-owner should get 403 on GET")
}

// TestListenerAuth_Owner_ConfirmOrder verifies that the order creator can
// confirm their own order via PATCH /api/v1/orders/{id}/status.
func TestListenerAuth_Owner_ConfirmOrder(t *testing.T) {
	r, c := initListenerRouter(t)
	const owner = "user-owner-c"

	orderID := createOrderAs(t, r, c, owner, []string{dto.RoleCustomer}, "confirmable-widget")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/orders/"+orderID+"/status",
		strings.NewReader(`{"status":"confirmed"}`))
	req = req.WithContext(withAuthorizer(auth.TestContext(owner, []string{dto.RoleCustomer}), c))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "order owner should get 200 on PATCH confirm")
}

// TestListenerAuth_CrossOwner_ConfirmOrder verifies that a non-owner subject
// receives 403 when attempting to confirm another user's order.
func TestListenerAuth_CrossOwner_ConfirmOrder(t *testing.T) {
	r, c := initListenerRouter(t)
	const owner = "user-owner-d"
	const intruder = "user-intruder-d"

	orderID := createOrderAs(t, r, c, owner, []string{dto.RoleCustomer}, "cross-confirm-widget")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/orders/"+orderID+"/status",
		strings.NewReader(`{"status":"confirmed"}`))
	req = req.WithContext(withAuthorizer(auth.TestContext(intruder, []string{dto.RoleCustomer}), c))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "non-owner should get 403 on PATCH confirm")
}

// TestListenerAuth_NonCustomer_Create verifies that a non-customer subject
// receives 403 when attempting to create an order.
func TestListenerAuth_NonCustomer_Create(t *testing.T) {
	r, c := initListenerRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/",
		strings.NewReader(`{"item":"blocked-widget"}`))
	req = req.WithContext(withAuthorizer(auth.TestContext("user-viewer", []string{"viewer"}), c))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "non-customer should get 403 on POST")
}

// TestListenerAuth_NonCustomer_List verifies that a non-customer subject
// receives 403 on GET /api/v1/orders/.
func TestListenerAuth_NonCustomer_List(t *testing.T) {
	r, c := initListenerRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/", nil)
	req = req.WithContext(withAuthorizer(auth.TestContext("user-viewer", []string{"viewer"}), c))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "non-customer should get 403 on GET list")
}

// TestListenerAuth_NoAuthorizer_FailClosed verifies that a request without an
// Authorizer in context is denied (fail-closed 403), not permitted.
func TestListenerAuth_NoAuthorizer_FailClosed(t *testing.T) {
	r, _ := initListenerRouter(t)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"create without authorizer → 403", http.MethodPost, "/api/v1/orders/"},
		{"list without authorizer → 403", http.MethodGet, "/api/v1/orders/"},
		{"get without authorizer → 403", http.MethodGet, "/api/v1/orders/some-order"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var req *http.Request
			if tt.method == http.MethodPost {
				req = httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{"item":"x"}`))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			// Inject principal but NO Authorizer → RequirePermission fail-closed.
			req = req.WithContext(auth.TestContext("user-a", []string{dto.RoleCustomer}))
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusForbidden, rec.Code, tt.name)
		})
	}
}

// TestListenerAuth_Unauthenticated_Returns401 verifies that a request with
// no principal at all is rejected with 401 (unauthenticated).
func TestListenerAuth_Unauthenticated_Returns401(t *testing.T) {
	r, c := initListenerRouter(t)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"create no principal", http.MethodPost, "/api/v1/orders/"},
		{"list no principal", http.MethodGet, "/api/v1/orders/"},
		{"get no principal", http.MethodGet, "/api/v1/orders/some-order"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var req *http.Request
			if tt.method == http.MethodPost {
				req = httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{"item":"x"}`))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			// WithAuthorizer but no principal → RequirePermission returns 401.
			req = req.WithContext(auth.WithAuthorizer(context.Background(), c.Authorizer()))
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, tt.name)
		})
	}
}
