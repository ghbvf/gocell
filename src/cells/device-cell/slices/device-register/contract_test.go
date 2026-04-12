package deviceregister

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturePub implements outbox.Publisher and records all published events.
type capturePub struct {
	mu      sync.Mutex
	entries []capturedEvent
}
type capturedEvent struct {
	Topic   string
	Payload json.RawMessage
}

func (p *capturePub) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = append(p.entries, capturedEvent{Topic: topic, Payload: payload})
	return nil
}

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

// Contract: http.device.v1 — error path returns {error: {code, message}}.
func TestHttpDeviceV1Serve_ErrorEnvelope(t *testing.T) {
	h := setupRegisterHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/",
		strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleRegister(w, req)

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

// Contract: event.device-registered.v1 — registration publishes {id, name, status}.
func TestEventDeviceRegisteredV1Publish(t *testing.T) {
	pub := &capturePub{}
	repo := mem.NewDeviceRepository()
	svc := NewService(repo, pub, slog.Default())
	h := NewHandler(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/",
		strings.NewReader(`{"name":"evt-sensor"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleRegister(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	require.Len(t, pub.entries, 1, "expected 1 published event")
	assert.Equal(t, TopicDeviceRegistered, pub.entries[0].Topic)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(pub.entries[0].Payload, &payload))
	assert.NotEmpty(t, payload["id"], "payload requires id")
	assert.Equal(t, "evt-sensor", payload["name"], "payload requires name")
	assert.NotEmpty(t, payload["status"], "payload requires status")
}
