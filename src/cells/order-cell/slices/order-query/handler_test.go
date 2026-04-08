package orderquery

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
)

func newTestHandler(orders ...*domain.Order) (*Handler, *mem.OrderRepository) {
	repo := mem.NewOrderRepository()
	for _, o := range orders {
		_ = repo.Create(context.Background(), o)
	}
	svc := NewService(repo, slog.Default())
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

func TestHandleList(t *testing.T) {
	tests := []struct {
		name       string
		seed       []*domain.Order
		wantStatus int
		wantTotal  string
	}{
		{
			name:       "empty list returns 200",
			seed:       nil,
			wantStatus: http.StatusOK,
			wantTotal:  `"total":0`,
		},
		{
			name: "populated list returns 200",
			seed: []*domain.Order{
				{ID: "ord-a", Item: "a", Status: "pending"},
				{ID: "ord-b", Item: "b", Status: "pending"},
			},
			wantStatus: http.StatusOK,
			wantTotal:  `"total":2`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(tt.seed...)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/", nil)

			h.HandleList(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), tt.wantTotal)
		})
	}
}
