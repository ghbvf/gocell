package deviceregister

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpDeviceRegisterV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.register.v1")

	c.ValidateRequest(t, []byte(`{"name":"sensor-01"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"d-1","name":"sensor-01","status":"registered"}}`))
	c.MustRejectRequest(t, []byte(`{"name":"a","extra":"bad"}`))
}

func TestEventDeviceRegisteredV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.device-registered.v1")

	c.ValidatePayload(t, []byte(`{"id":"d-1","name":"sensor-01","status":"registered"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-dr-1"}`))
	c.MustRejectPayload(t, []byte(`{"id":"d-1"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
