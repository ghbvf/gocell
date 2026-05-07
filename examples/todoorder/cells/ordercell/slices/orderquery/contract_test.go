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

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	getv1 "github.com/ghbvf/gocell/generated/contracts/http/order/get/v1"
	listv1 "github.com/ghbvf/gocell/generated/contracts/http/order/list/v1"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/tests/contracttest"
)

var allowAllContractPolicy = func(*http.Request) error { return nil }

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
	h := getv1.NewHandler(svc, allowAllContractPolicy)

	// r.PathValue relies on stdlib ServeMux pattern routing; mount through
	// http.NewServeMux so the {id} placeholder is populated.
	mux := http.NewServeMux()
	mux.Handle(c.HTTP.Method+" "+c.HTTP.Path, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, strings.Replace(c.HTTP.Path, "{id}", "ord-contract-get", 1), nil)
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpOrderGetV1Serve_NotFound(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.get.v1")
	svc := newContractQuerySvc()
	h := getv1.NewHandler(svc, allowAllContractPolicy)

	mux := http.NewServeMux()
	mux.Handle(c.HTTP.Method+" "+c.HTTP.Path, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, strings.Replace(c.HTTP.Path, "{id}", "missing-order", 1), nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, http.StatusNotFound, rec.Body.Bytes())
}

func TestHttpOrderListV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.list.v1")
	svc := newContractQuerySvc(
		&domain.Order{ID: "ord-a", Item: "widget", Status: "pending", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		&domain.Order{ID: "ord-b", Item: "gizmo", Status: "pending", CreatedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)},
	)
	h := listv1.NewHandler(svc, allowAllContractPolicy)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=2", nil)
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}
