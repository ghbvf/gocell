package auditappend

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventAuditAppendedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.audit.appended.v1")

	c.ValidatePayload(t, []byte(`{"audit_entry_id":"audit-001","event_type":"session.created"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-audit-1"}`))
	c.MustRejectPayload(t, []byte(`{"audit_entry_id":"a"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventSessionCreatedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.session.created.v1")

	c.ValidatePayload(t, []byte(`{"session_id":"s-1","user_id":"u-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-s1"}`))
}

func TestEventSessionRevokedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.session.revoked.v1")

	c.ValidatePayload(t, []byte(`{"session_id":"sess-1","user_id":"usr-1"}`))
	c.MustRejectPayload(t, []byte(`{"session_id":"s"}`))
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

func TestEventConfigChangedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")

	c.ValidatePayload(t, []byte(`{"action":"created","key":"k","value":"v","version":1}`))
	c.MustRejectPayload(t, []byte(`{"action":"created"}`))
}

func TestEventConfigRollbackV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.rollback.v1")

	c.ValidatePayload(t, []byte(`{"action":"rollback","key":"k","target_version":1,"new_version":2}`))
	c.MustRejectPayload(t, []byte(`{"action":"rollback","key":"k"}`))
}
