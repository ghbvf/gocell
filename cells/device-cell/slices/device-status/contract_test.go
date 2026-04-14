package devicestatus

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpDeviceStatusV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.status.v1")

	c.ValidateResponse(t, []byte(`{"data":{"id":"d-1","name":"sensor-01","status":"online","lastSeen":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
