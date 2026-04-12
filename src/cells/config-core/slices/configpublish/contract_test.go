package configpublish

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventConfigChangedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")

	c.ValidatePayload(t, []byte(`{"action":"published","key":"app.name","config_id":"cfg-1","version":2}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-456"}`))
}

func TestEventConfigRollbackV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.rollback.v1")

	c.ValidatePayload(t, []byte(`{"action":"rollback","key":"app.name","target_version":1,"new_version":3}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-789"}`))
	c.MustRejectPayload(t, []byte(`{"action":"rollback","key":"app.name"}`))
}
