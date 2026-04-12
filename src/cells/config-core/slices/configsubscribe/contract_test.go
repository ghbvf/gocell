package configsubscribe

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventConfigChangedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")

	c.ValidatePayload(t, []byte(`{"action":"updated","key":"app.name","value":"newval","version":2}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-sub-1"}`))
}
