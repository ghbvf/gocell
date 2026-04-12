package orderquery

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/contracttest"
)

func newContractQueryHandler(orders ...*domain.Order) http.Handler {
	repo := mem.NewOrderRepository()
	for _, order := range orders {
		_ = repo.Create(context.Background(), order)
	}
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("q"), 32))
	svc := NewService(repo, codec, slog.Default())
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/orders/", http.HandlerFunc(h.HandleList))
	mux.Handle("GET /api/v1/orders/{id}", http.HandlerFunc(h.HandleGet))
	return mux
}

func TestHttpOrderGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.order.get.v1")
	h := newContractQueryHandler(&domain.Order{
		ID:        "ord-contract-get",
		Item:      "widget",
		Status:    "pending",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, strings.Replace(c.HTTP.Path, "{id}", "ord-contract-get", 1), nil)
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpOrderListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.order.list.v1")
	h := newContractQueryHandler(
		&domain.Order{ID: "ord-a", Item: "widget", Status: "pending", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		&domain.Order{ID: "ord-b", Item: "gizmo", Status: "pending", CreatedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=2", nil)
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}
