package devicestatus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: http.device.v1 — GET /devices/{id}/status returns {data: {id, name, status, lastSeen}}.
func TestHttpDeviceV1Serve(t *testing.T) {
	h, repo := setupStatusHandler()

	// Seed device.
	require.NoError(t, repo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/dev-1/status", nil)
	req.SetPathValue("id", "dev-1")
	h.HandleGetStatus(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "dev-1", resp.Data.ID, "contract requires id")
	assert.Equal(t, "sensor-a", resp.Data.Name, "contract requires name")
	assert.Equal(t, "online", resp.Data.Status, "contract requires status")
}
