package configsubscribe

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventConfigEntryWrittenV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.entry-written.v1")

	c.ValidatePayload(t, []byte(`{"action":"updated","key":"app.name","value":"newval","version":2}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-sub-1"}`))
	c.MustRejectPayload(t, []byte(`{"action":"updated"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigVersionPublishedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.version-published.v1")

	c.ValidatePayload(t, []byte(`{"key":"app.name","configId":"cfg-1","version":2,"sensitive":false}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-vp-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
