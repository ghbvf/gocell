package orderquery

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/query"
)

func TestToOrderResponse_NilInput(t *testing.T) {
	var got OrderResponse
	assert.NotPanics(t, func() { got = toOrderResponse(nil) })
	assert.Zero(t, got.ID)
}

func TestOrderResponse_Fields(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	order := &domain.Order{
		ID: "ord-1", Item: "laptop", Status: "pending", CreatedAt: now,
	}
	resp := toOrderResponse(order)

	assert.Equal(t, "ord-1", resp.ID)
	assert.Equal(t, "laptop", resp.Item)
	assert.Equal(t, "pending", resp.Status)
	assert.Equal(t, now, resp.CreatedAt)

	// Verify camelCase JSON keys.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"id"`)
	assert.Contains(t, s, `"item"`)
	assert.Contains(t, s, `"status"`)
	assert.Contains(t, s, `"createdAt"`)
}

func newTestHandler(orders ...*domain.Order) (*Handler, *mem.OrderRepository) {
	repo := mem.NewOrderRepository()
	for _, o := range orders {
		_ = repo.Create(context.Background(), o)
	}
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc := NewService(repo, codec, slog.Default())
	return NewHandler(svc), repo
}

func TestHandleGet(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		seed       []*domain.Order
		wantStatus int
	}{
		{
			name:       "found returns 200",
			id:         "ord-1",
			seed:       []*domain.Order{{ID: "ord-1", Item: "widget", Status: "pending"}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "not found returns 404",
			id:         "ord-missing",
			seed:       nil,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(tt.seed...)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+tt.id, nil)
			req.SetPathValue("id", tt.id)

			h.HandleGet(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		})
	}
}

func TestHandleGet_ResponseBody(t *testing.T) {
	h, _ := newTestHandler(&domain.Order{ID: "ord-detail", Item: "laptop", Status: "confirmed"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/ord-detail", nil)
	req.SetPathValue("id", "ord-detail")

	h.HandleGet(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"id":"ord-detail"`)
	assert.Contains(t, body, `"item":"laptop"`)
}

func TestHandleList_Default(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var seed []*domain.Order
	for i := 0; i < 3; i++ {
		seed = append(seed, &domain.Order{
			ID:        "ord-" + string(rune('a'+i)),
			Item:      "item",
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	h, _ := newTestHandler(seed...)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders", nil)

	h.HandleList(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"hasMore"`)
	assert.Contains(t, body, `"data"`)
}

func TestHandleList_WithLimit(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var seed []*domain.Order
	for i := 0; i < 10; i++ {
		seed = append(seed, &domain.Order{
			ID:        "ord-" + string(rune('a'+i)),
			Item:      "item",
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	h, _ := newTestHandler(seed...)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders?limit=2", nil)

	h.HandleList(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data := resp["data"].([]any)
	assert.Len(t, data, 2)
	assert.Equal(t, true, resp["hasMore"])
	assert.NotEmpty(t, resp["nextCursor"])
}

func TestHandleList_InvalidLimit(t *testing.T) {
	h, _ := newTestHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders?limit=abc", nil)

	h.HandleList(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandleList_ExceedsMaxLimit(t *testing.T) {
	h, _ := newTestHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders?limit=501", nil)

	h.HandleList(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandleList_InvalidCursor(t *testing.T) {
	h, _ := newTestHandler(&domain.Order{ID: "ord-1", Item: "x", Status: "pending"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders?cursor=garbage", nil)

	h.HandleList(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_CURSOR_INVALID")
}

func TestHandleList_Empty(t *testing.T) {
	h, _ := newTestHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders", nil)

	h.HandleList(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	data := resp["data"].([]any)
	assert.Empty(t, data)
	assert.Equal(t, false, resp["hasMore"])
}

func TestHandleList_Pagination_FullTraversal(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var seed []*domain.Order
	for i := 0; i < 7; i++ {
		seed = append(seed, &domain.Order{
			ID:        "ord-" + string(rune('a'+i)),
			Item:      "item",
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	h, _ := newTestHandler(seed...)

	var allIDs []string
	cursor := ""

	for page := 0; page < 10; page++ {
		url := "/api/v1/orders?limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)

		h.HandleList(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		data := resp["data"].([]any)
		for _, item := range data {
			m := item.(map[string]any)
			id, ok := m["id"].(string)
				require.True(t, ok, "response item should have string 'id' field")
				allIDs = append(allIDs, id)
		}

		hasMore := resp["hasMore"].(bool)
		if !hasMore {
			break
		}
		cursor = resp["nextCursor"].(string)
		require.NotEmpty(t, cursor)
	}

	// All 7 items collected, no duplicates
	assert.Len(t, allIDs, 7)
	seen := make(map[string]bool)
	for _, id := range allIDs {
		assert.False(t, seen[id], "duplicate ID: %s", id)
		seen[id] = true
	}
}
