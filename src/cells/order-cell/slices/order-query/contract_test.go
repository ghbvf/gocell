package orderquery

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpOrderGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.order.get.v1")

	c.ValidateResponse(t, []byte(`{"data":{"id":"o-1","item":"widget","status":"created","createdAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpOrderListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.order.list.v1")

	c.ValidateResponse(t, []byte(`{"data":[{"id":"o-1","item":"widget","status":"created","createdAt":"2026-01-01T00:00:00Z"}],"hasMore":false}`))
	c.MustRejectResponse(t, []byte(`{"data":"not-array","hasMore":false}`))
}
