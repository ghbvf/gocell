package ordercreate

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
)

func TestToOrderCreateResponse_NilInput(t *testing.T) {
	assert.NotPanics(t, func() { toOrderCreateResponse(nil) })
}

func TestOrderCreateResponse_Fields(t *testing.T) {
	order := &domain.Order{ID: "ord-1", Item: "laptop", Status: "pending"}
	resp := toOrderCreateResponse(order)

	assert.Equal(t, "ord-1", resp.ID)
	assert.Equal(t, "laptop", resp.Item)
	assert.Equal(t, "pending", resp.Status)

	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"id"`)
	assert.Contains(t, s, `"item"`)
	assert.Contains(t, s, `"status"`)
}

func newTestHandler() *Handler {
	repo := mem.NewOrderRepository()
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	return NewHandler(svc)
}

func TestHandleCreate(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "success returns 201",
			body:       `{"item":"widget"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "empty item returns 400",
			body:       `{"item":""}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{invalid`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing item field returns 400",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown field returns 400",
			body:       `{"item":"x","extra":"y"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")

			h.HandleCreate(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		})
	}
}

func TestHandleCreate_ResponseBody(t *testing.T) {
	h := newTestHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(`{"item":"laptop"}`))
	req.Header.Set("Content-Type", "application/json")

	h.HandleCreate(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp struct {
		Data struct {
			ID     string `json:"id"`
			Item   string `json:"item"`
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.ID)
	assert.Equal(t, "laptop", resp.Data.Item)
	assert.Equal(t, "pending", resp.Data.Status)

	// Verify camelCase JSON keys (#27n).
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	var dataMap map[string]any
	require.NoError(t, json.Unmarshal(raw["data"], &dataMap))
	assert.Contains(t, dataMap, "id", "key must be camelCase")
	assert.Contains(t, dataMap, "item", "key must be camelCase")
	assert.Contains(t, dataMap, "status", "key must be camelCase")
}
