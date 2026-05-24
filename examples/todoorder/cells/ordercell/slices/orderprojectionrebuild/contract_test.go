package orderprojectionrebuild

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	internalproj "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/orderprojection"
	projectionrebuild "github.com/ghbvf/gocell/generated/contracts/http/order/internalapi/projection-rebuild/v1"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

func TestHttpOrderProjectionRebuildV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "todoorder")
	c := contracttest.LoadByID(t, root, "http.order.internal.projection-rebuild.v1")

	svc, err := internalproj.NewService()
	require.NoError(t, err)
	h := projectionrebuild.NewHandler(NewRebuildAdapter(svc), func(*http.Request) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestServiceContext("ordercell"))
	h.ServeHTTP(rec, req)

	c.ValidateHTTPResponseRecorder(t, rec)
}
