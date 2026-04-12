package orderquery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: http.order.v1 — GET /{id} returns {data: Order}, GET / returns paginated list.
func TestHttpOrderV1Serve(t *testing.T) {
	now := time.Now()
	h, _ := newTestHandler(&domain.Order{
		ID: "ord-1", Item: "widget", Status: "pending", CreatedAt: now,
	})

	t.Run("get single", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ord-1", nil)
		req.SetPathValue("id", "ord-1")
		h.HandleGet(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp, "data", "contract requires data envelope")
	})

	t.Run("list", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		h.HandleList(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp, "data", "contract requires data array")
		assert.Contains(t, resp, "hasMore", "contract requires hasMore field")
	})
}
