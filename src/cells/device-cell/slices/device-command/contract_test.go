package devicecommand

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: http.device.v1 — POST /devices/{id}/commands returns {data: {id, deviceId, payload, status}}.
func TestHttpDeviceV1Serve(t *testing.T) {
	h, _, _ := setupCommandHandler() // seeds dev-1

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/commands",
		strings.NewReader(`{"payload":"reboot"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "dev-1")
	h.HandleEnqueue(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data struct {
			ID       string `json:"id"`
			DeviceID string `json:"deviceId"`
			Payload  string `json:"payload"`
			Status   string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.ID, "contract requires id")
	assert.Equal(t, "dev-1", resp.Data.DeviceID, "contract requires deviceId")
	assert.Equal(t, "reboot", resp.Data.Payload, "contract requires payload")
	assert.NotEmpty(t, resp.Data.Status, "contract requires status")
}

// Contract: http.device.v1 — error path returns {error: {code, message}}.
func TestHttpDeviceV1Serve_ErrorEnvelope(t *testing.T) {
	h, _, _ := setupCommandHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/commands",
		strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "dev-1")
	h.HandleEnqueue(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Error.Code, "contract requires error.code")
	assert.NotEmpty(t, resp.Error.Message, "contract requires error.message")
}

// Contract: command.device-command.v1 — device command lifecycle (enqueue, list, ack).
func TestCommandDeviceCommandV1Handle(t *testing.T) {
	h, _, _ := setupCommandHandler() // seeds dev-1

	// Enqueue a command.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/commands",
		strings.NewReader(`{"payload":"update-fw"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "dev-1")
	h.HandleEnqueue(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	// List pending commands.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/dev-1/commands", nil)
	req.SetPathValue("id", "dev-1")
	h.HandleListPending(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var listResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	assert.Contains(t, listResp, "data", "contract requires data array")

	// Ack the command.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/commands/"+created.Data.ID+"/ack", nil)
	req.SetPathValue("id", "dev-1")
	req.SetPathValue("cmdId", created.Data.ID)
	h.HandleAck(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "contract: ack must succeed")
}
