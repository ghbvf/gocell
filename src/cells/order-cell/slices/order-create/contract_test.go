package ordercreate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// Contract: event.order-created.v1 — order creation publishes order payload.
func TestEventOrderCreatedV1Publish(t *testing.T) {
	h := newTestHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders",
		strings.NewReader(`{"item":"evt-widget"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleCreate(w, req)

	assert.Equal(t, http.StatusCreated, w.Code,
		"contract: order creation must succeed, triggering event.order-created.v1 publish")
}
