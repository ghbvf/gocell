package ordercreate

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpOrderCreateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.order.create.v1")

	c.ValidateRequest(t, []byte(`{"item":"widget"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"o-1","item":"widget","status":"pending"}}`))
	c.MustRejectRequest(t, []byte(`{"item":"x","extra":"bad"}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestEventOrderCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.order-created.v1")

	c.ValidatePayload(t, []byte(`{"id":"o-1","item":"widget","status":"pending"}`))
	c.MustRejectPayload(t, []byte(`{"id":"o-1"}`))
	// Headers validation skipped: order-cell uses publisher.Publish directly
	// (no outbox.Entry), so event_id is not emitted at the transport level.
	// Headers schema documents the outbox transport contract for future L2 migration.
}
