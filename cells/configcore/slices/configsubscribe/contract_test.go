package configsubscribe

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventConfigEntryUpsertedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.config.entry-upserted.v1")

	// Metadata-only schema: key + version + actorId; no value field.
	c.ValidatePayload(t, []byte(`{"key":"app.name","version":2,"actorId":"adm-1"}`))
	c.ValidatePayload(t, []byte(`{"key":"app.name","version":1,"actorId":"adm-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-sub-1"}`))

	// Missing required fields
	c.MustRejectPayload(t, []byte(`{"version":2,"actorId":"adm-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name","actorId":"adm-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name","version":2}`)) // missing actorId

	// Invalid key values
	c.MustRejectPayload(t, []byte(`{"key":"","version":2,"actorId":"adm-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"   ","version":2,"actorId":"adm-1"}`))

	// Invalid version
	c.MustRejectPayload(t, []byte(`{"key":"app.name","version":0,"actorId":"adm-1"}`))

	// value field must be rejected (metadata-only schema, additionalProperties: false)
	c.MustRejectPayload(t, []byte(`{"key":"app.name","value":"newval","version":2,"actorId":"adm-1"}`))

	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigEntryDeletedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.config.entry-deleted.v1")

	// Valid: key + version + actorId (metadata-only + tombstone protection).
	c.ValidatePayload(t, []byte(`{"key":"app.name","version":1,"actorId":"adm-1"}`))
	c.ValidatePayload(t, []byte(`{"key":"app.name","version":42,"actorId":"adm-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-del-1"}`))

	// Missing required fields.
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectPayload(t, []byte(`{"key":"app.name","actorId":"adm-1"}`)) // missing version
	c.MustRejectPayload(t, []byte(`{"version":1,"actorId":"adm-1"}`))      // missing key
	c.MustRejectPayload(t, []byte(`{"key":"app.name","version":1}`))       // missing actorId
	c.MustRejectPayload(t, []byte(`{"key":"","version":1,"actorId":"adm-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"   ","version":1,"actorId":"adm-1"}`))

	// Invalid version.
	c.MustRejectPayload(t, []byte(`{"key":"app.name","version":0,"actorId":"adm-1"}`))

	// Additional properties forbidden.
	c.MustRejectPayload(t, []byte(`{"key":"app.name","version":1,"actorId":"adm-1","value":"old"}`))

	c.MustRejectHeaders(t, []byte(`{}`))
}
