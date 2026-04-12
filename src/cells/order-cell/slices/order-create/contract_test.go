package ordercreate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
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

// Contract: http.order.v1 — POST returns {data: {id, item, status}}.
func TestHttpOrderV1Serve(t *testing.T) {
	h := newTestHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders",
		strings.NewReader(`{"item":"widget"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleCreate(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data struct {
			ID     string `json:"id"`
			Item   string `json:"item"`
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.ID, "contract requires id")
	assert.Equal(t, "widget", resp.Data.Item, "contract requires item")
	assert.NotEmpty(t, resp.Data.Status, "contract requires status")
}

// Contract: http.order.v1 — error path returns {error: {code, message}}.
func TestHttpOrderV1Serve_ErrorEnvelope(t *testing.T) {
	h := newTestHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders",
		strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleCreate(w, req)

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

// Contract: event.order-created.v1 — order creation publishes {id, item, status}.
func TestEventOrderCreatedV1Publish(t *testing.T) {
	pub := &capturePub{}
	repo := mem.NewOrderRepository()
	svc := NewService(repo, pub, slog.Default())
	h := NewHandler(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders",
		strings.NewReader(`{"item":"evt-widget"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleCreate(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	require.Len(t, pub.entries, 1, "expected 1 published event")
	assert.Equal(t, TopicOrderCreated, pub.entries[0].Topic)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(pub.entries[0].Payload, &payload))
	assert.NotEmpty(t, payload["id"], "payload requires id")
	assert.Equal(t, "evt-widget", payload["item"], "payload requires item")
	assert.NotEmpty(t, payload["status"], "payload requires status")
}
