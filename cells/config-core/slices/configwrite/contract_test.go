package configwrite

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventConfigChangedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")

	c.ValidatePayload(t, []byte(`{"action":"created","key":"app.name","value":"myapp","version":1}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-123"}`))
	c.MustRejectPayload(t, []byte(`{"action":"created","key":"app.name"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
