package auditappend

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventAuditAppendedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.audit.appended.v1")

	c.ValidatePayload(t, []byte(`{"auditEntryId":"audit-001","eventType":"session.created"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-audit-1"}`))
	c.MustRejectPayload(t, []byte(`{"auditEntryId":"a"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventSessionCreatedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.session.created.v1")

	c.ValidatePayload(t, []byte(`{"sessionId":"s-1","userId":"u-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-s1"}`))
}

func TestEventSessionRevokedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.session.revoked.v1")

	c.ValidatePayload(t, []byte(`{"sessionId":"sess-1","userId":"usr-1"}`))
	c.MustRejectPayload(t, []byte(`{"sessionId":"s"}`))
}

func TestEventUserCreatedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.user.created.v1")

	c.ValidatePayload(t, []byte(`{"userId":"usr-1","username":"alice"}`))
	// snake_case user_id is rejected — schema migrated to camelCase (G.6)
	c.MustRejectPayload(t, []byte(`{"user_id":"x"}`))
	c.MustRejectPayload(t, []byte(`{"userId":"x"}`)) // missing required username
}

func TestEventUserLockedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.user.locked.v1")

	// actorId required since G.1 + G.6 migration
	c.ValidatePayload(t, []byte(`{"userId":"usr-1","actorId":"admin-1"}`))
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectPayload(t, []byte(`{"userId":"usr-1"}`)) // missing required actorId
}

func TestEventConfigEntryUpsertedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.entry-upserted.v1")

	// actorId required since G.2 migration
	c.ValidatePayload(t, []byte(`{"key":"k","version":1,"actorId":"admin-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"k","version":1}`)) // missing required actorId
	c.MustRejectPayload(t, []byte(`{"key":"k","value":"v","version":1,"actorId":"a"}`))
	c.MustRejectPayload(t, []byte(`{"key":"","version":1,"actorId":"a"}`))
	c.MustRejectPayload(t, []byte(`{"key":"   ","version":1,"actorId":"a"}`))
	c.MustRejectPayload(t, []byte(`{"key":"k","version":0,"actorId":"a"}`))
}

func TestEventConfigEntryDeletedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.entry-deleted.v1")

	// Valid: key + version + actorId required (G.2 migration).
	c.ValidatePayload(t, []byte(`{"key":"k","version":1,"actorId":"admin-1"}`))
	c.ValidatePayload(t, []byte(`{"key":"k","version":7,"actorId":"admin-1"}`))

	// Missing required fields.
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectPayload(t, []byte(`{"key":"k","version":1}`))     // missing actorId
	c.MustRejectPayload(t, []byte(`{"key":"k","actorId":"a"}`))   // missing version
	c.MustRejectPayload(t, []byte(`{"version":1,"actorId":"a"}`)) // missing key
	c.MustRejectPayload(t, []byte(`{"key":"","actorId":"a"}`))
	c.MustRejectPayload(t, []byte(`{"key":"   ","actorId":"a"}`))

	// Invalid version.
	c.MustRejectPayload(t, []byte(`{"key":"k","version":0,"actorId":"a"}`))
}

func TestEventConfigVersionPublishedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.version-published.v1")

	// actorId required since G.2 migration
	c.ValidatePayload(t, []byte(`{"key":"k","configId":"cfg-1","version":1,"actorId":"admin-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"k","configId":"cfg-1","version":1}`))                                 // missing actorId
	c.MustRejectPayload(t, []byte(`{"key":"k","configId":"cfg-1","version":1,"actorId":"a","sensitive":false}`)) // additional property
	c.MustRejectPayload(t, []byte(`{"key":"k","actorId":"a"}`))                                                  // missing configId + version
}

func TestEventConfigRollbackV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.rollback.v1")

	// actorId required since G.2 migration
	c.ValidatePayload(t, []byte(`{"key":"k","targetVersion":1,"newVersion":2,"actorId":"admin-1"}`))
	c.MustRejectPayload(t, []byte(`{"key":"k","targetVersion":1,"newVersion":2}`)) // missing actorId
	c.MustRejectPayload(t, []byte(`{"key":"k"}`))
}
