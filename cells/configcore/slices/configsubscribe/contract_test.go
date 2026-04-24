package configsubscribe

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventConfigEntryUpsertedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.entry-upserted.v1")

	c.ValidatePayload(t, []byte(`{"key":"app.name","value":"newval","version":2}`))
	c.ValidatePayload(t, []byte(`{"key":"app.name","value":"","version":2}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-sub-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name","version":2}`))
	c.MustRejectPayload(t, []byte(`{"key":"","value":"newval","version":2}`))
	c.MustRejectPayload(t, []byte(`{"key":"   ","value":"newval","version":2}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name","value":"newval","version":0}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigEntryDeletedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.entry-deleted.v1")

	c.ValidatePayload(t, []byte(`{"key":"app.name"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-del-1"}`))
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectPayload(t, []byte(`{"key":""}`))
	c.MustRejectPayload(t, []byte(`{"key":"   "}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
