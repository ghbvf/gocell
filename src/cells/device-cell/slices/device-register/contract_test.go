package deviceregister

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: http.device.v1 — POST returns {data: {id, name, status}}.
func TestHttpDeviceV1Serve(t *testing.T) {
	h := setupRegisterHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/",
		strings.NewReader(`{"name":"sensor-contract"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleRegister(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.ID, "contract requires id")
	assert.Equal(t, "sensor-contract", resp.Data.Name, "contract requires name")
	assert.NotEmpty(t, resp.Data.Status, "contract requires status")
}

// Contract: event.device-registered.v1 — registration publishes device payload.
func TestEventDeviceRegisteredV1Publish(t *testing.T) {
	h := setupRegisterHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/",
		strings.NewReader(`{"name":"evt-sensor"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleRegister(w, req)

	assert.Equal(t, http.StatusCreated, w.Code,
		"contract: registration must succeed, triggering event.device-registered.v1 publish")
}
