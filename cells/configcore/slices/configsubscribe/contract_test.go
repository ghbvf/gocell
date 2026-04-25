package configsubscribe

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventConfigEntryUpsertedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.entry-upserted.v1")

	// Metadata-only schema: only key+version, no value field.
	c.ValidatePayload(t, []byte(`{"key":"app.name","version":2}`))
	c.ValidatePayload(t, []byte(`{"key":"app.name","version":1}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-sub-1"}`))

	// Missing required fields
	c.MustRejectPayload(t, []byte(`{"version":2}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name"}`))

	// Invalid key values
	c.MustRejectPayload(t, []byte(`{"key":"","version":2}`))
	c.MustRejectPayload(t, []byte(`{"key":"   ","version":2}`))

	// Invalid version
	c.MustRejectPayload(t, []byte(`{"key":"app.name","version":0}`))

	// value field must be rejected (metadata-only schema, additionalProperties: false)
	c.MustRejectPayload(t, []byte(`{"key":"app.name","value":"newval","version":2}`))

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
