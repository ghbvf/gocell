package configread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: http.config.get.v1 — GET /{key} returns {data: ConfigEntry}, GET / returns paginated list.
func TestHttpConfigGetV1Serve(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	t.Run("single entry", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/app.name", nil)
		handler.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp, "data", "contract requires data envelope")

		var data map[string]any
		require.NoError(t, json.Unmarshal(resp["data"], &data))
		assert.Contains(t, data, "Key", "contract requires Key field")
		assert.Contains(t, data, "Value", "contract requires Value field")
		assert.Contains(t, data, "Version", "contract requires Version field")
	})

	t.Run("list", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		handler.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Contains(t, resp, "data", "contract requires data array")
		assert.Contains(t, resp, "hasMore", "contract requires hasMore field")
	})
}
