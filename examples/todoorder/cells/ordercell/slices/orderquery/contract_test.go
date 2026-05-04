package orderquery

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	getv1 "github.com/ghbvf/gocell/generated/contracts/http/order/get/v1"
	listv1 "github.com/ghbvf/gocell/generated/contracts/http/order/list/v1"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
)

func newContractQuerySvc(orders ...*domain.Order) *Service {
	repo := mem.NewOrderRepository()
	for _, order := range orders {
		_ = repo.Create(context.Background(), order)
	}
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("q"), 32))
	svc, err := NewService(repo, codec, slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	return svc
}

func TestHttpOrderGetV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.get.v1")
	svc := newContractQuerySvc(&domain.Order{
		ID:        "ord-contract-get",
		Item:      "widget",
		Status:    "pending",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	h := getv1.NewHandler(svc, nil)

	// chi.URLParam relies on chi route context; tests must mount through chi.NewRouter().
	r := chi.NewRouter()
	r.Get("/api/v1/orders/{id}", h.ServeHTTP)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, strings.Replace(c.HTTP.Path, "{id}", "ord-contract-get", 1), nil)
	r.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpOrderListV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.list.v1")
	svc := newContractQuerySvc(
		&domain.Order{ID: "ord-a", Item: "widget", Status: "pending", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		&domain.Order{ID: "ord-b", Item: "gizmo", Status: "pending", CreatedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)},
	)
	h := listv1.NewHandler(svc, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=2", nil)
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}
