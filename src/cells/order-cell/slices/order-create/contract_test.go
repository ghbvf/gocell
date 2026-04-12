package ordercreate

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpOrderCreateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.order.create.v1")

	c.ValidateRequest(t, []byte(`{"item":"widget"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"o-1","item":"widget","status":"created"}}`))
	c.MustRejectRequest(t, []byte(`{"item":"x","extra":"bad"}`))
}

func TestEventOrderCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	_ = contracttest.LoadByID(t, root, "event.order-created.v1")
}
