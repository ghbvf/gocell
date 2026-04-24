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

	c.ValidatePayload(t, []byte(`{"user_id":"usr-1","username":"alice"}`))
	c.MustRejectPayload(t, []byte(`{"user_id":"x"}`))
}

func TestEventUserLockedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.user.locked.v1")

	c.ValidatePayload(t, []byte(`{"user_id":"usr-1"}`))
	c.MustRejectPayload(t, []byte(`{}`))
}

func TestEventConfigEntryWrittenV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.entry-written.v1")

	c.ValidatePayload(t, []byte(`{"action":"created","key":"k","value":"v","version":1}`))
	c.MustRejectPayload(t, []byte(`{"action":"created"}`))
}

func TestEventConfigVersionPublishedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.version-published.v1")

	c.ValidatePayload(t, []byte(`{"key":"k","configId":"cfg-1","version":1,"sensitive":false}`))
	c.MustRejectPayload(t, []byte(`{"key":"k"}`))
}

func TestEventConfigRollbackV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.rollback.v1")

	c.ValidatePayload(t, []byte(`{"key":"k","targetVersion":1,"newVersion":2}`))
	c.MustRejectPayload(t, []byte(`{"key":"k"}`))
}
