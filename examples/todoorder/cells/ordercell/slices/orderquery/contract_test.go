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
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/require"
)

func TestSpecOrderQueryMatchesContracts(t *testing.T) {
	root := contracttest.ExampleContractsRoot("todoorder")

	get := contracttest.LoadByID(t, root, "http.order.get.v1")
	require.NotNil(t, get.HTTP)
	require.Equal(t, get.ID, specOrderGet.ID)
	require.Equal(t, get.HTTP.Method, specOrderGet.Method)
	require.Equal(t, get.HTTP.Path, specOrderGet.Path)

	list := contracttest.LoadByID(t, root, "http.order.list.v1")
	require.NotNil(t, list.HTTP)
	require.Equal(t, list.ID, specOrderList.ID)
	require.Equal(t, list.HTTP.Method, specOrderList.Method)
	require.Equal(t, list.HTTP.Path, specOrderList.Path)
}

func newContractQueryHandler(orders ...*domain.Order) http.Handler {
	repo := mem.NewOrderRepository()
	for _, order := range orders {
		_ = repo.Create(context.Background(), order)
	}
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("q"), 32))
	svc, err := NewService(repo, codec, slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/orders/", http.HandlerFunc(h.HandleList))
	mux.Handle("GET /api/v1/orders/{id}", http.HandlerFunc(h.HandleGet))
	return mux
}

func TestHttpOrderGetV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot("todoorder")
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
	root := contracttest.ExampleContractsRoot("todoorder")
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
