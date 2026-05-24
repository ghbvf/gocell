package orderprojection

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	projectionsummary "github.com/ghbvf/gocell/generated/contracts/http/order/projection-summary/v1"
	projectionrebuild "github.com/ghbvf/gocell/generated/contracts/http/order/projection-rebuild/v1"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
	"github.com/stretchr/testify/require"
)

func TestHttpOrderProjectionSummaryV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.projection-summary.v1")

	svc, err := NewService(slog.Default())
	require.NoError(t, err)
	h := projectionsummary.NewHandler(NewSummaryAdapter(svc), func(*http.Request) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	h.ServeHTTP(rec, req)

	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpOrderProjectionRebuildV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.projection-rebuild.v1")

	svc, err := NewService(slog.Default())
	require.NoError(t, err)
	h := projectionrebuild.NewHandler(NewRebuildAdapter(svc), func(*http.Request) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestServiceContext("ordercell"))
	h.ServeHTTP(rec, req)

	c.ValidateHTTPResponseRecorder(t, rec)
}
