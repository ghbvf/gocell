package configwrite

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// Contract: event.config.changed.v1 — config create publishes {action, key, value, version}.
func TestEventConfigChangedV1Publish(t *testing.T) {
	pub := &capturePub{}
	repo := mem.NewConfigRepository()
	svc := NewService(repo, pub, slog.Default())
	mux := http.NewServeMux()
	h := NewHandler(svc)
	mux.HandleFunc("POST /", h.HandleCreate)

	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"gocell"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Len(t, pub.entries, 1, "expected 1 published event")
	assert.Equal(t, TopicConfigChanged, pub.entries[0].Topic)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(pub.entries[0].Payload, &payload))
	assert.Equal(t, "created", payload["action"], "payload requires action")
	assert.Equal(t, "app.name", payload["key"], "payload requires key")
	assert.Equal(t, "gocell", payload["value"], "payload requires value")
	assert.NotNil(t, payload["version"], "payload requires version")
}

// Contract: config write — error path returns {error: {code, message}}.
func TestHttpConfigWriteV1Serve_ErrorEnvelope(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

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
