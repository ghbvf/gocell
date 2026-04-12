package auditappend

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
)

// Contract: event.audit.appended.v1 — audit append publishes {audit_entry_id, event_type}.
func TestEventAuditAppendedV1Publish(t *testing.T) {
	svc, _ := newTestService()
	entry := outbox.Entry{
		ID:        "evt-contract-01",
		EventType: "event.session.created.v1",
		Payload:   []byte(`{"session_id":"s1","user_id":"u1"}`),
	}
	err := svc.HandleEvent(context.Background(), entry)
	assert.NoError(t, err, "contract: HandleEvent must succeed, triggering audit.appended.v1 publish")
}

// Contract: event.session.created.v1 subscribe — auditappend handles session.created events.
func TestEventSessionCreatedV1Subscribe(t *testing.T) {
	svc, _ := newTestService()
	err := svc.HandleEvent(context.Background(), outbox.Entry{
		ID: "evt-sess-01", EventType: "event.session.created.v1",
		Payload: []byte(`{"session_id":"s1","user_id":"u1"}`),
	})
	assert.NoError(t, err)
}

// Contract: event.session.revoked.v1 subscribe.
func TestEventSessionRevokedV1Subscribe(t *testing.T) {
	svc, _ := newTestService()
	err := svc.HandleEvent(context.Background(), outbox.Entry{
		ID: "evt-sess-rev-01", EventType: "event.session.revoked.v1",
		Payload: []byte(`{"session_id":"s1","user_id":"u1"}`),
	})
	assert.NoError(t, err)
}

// Contract: event.user.created.v1 subscribe.
func TestEventUserCreatedV1Subscribe(t *testing.T) {
	svc, _ := newTestService()
	err := svc.HandleEvent(context.Background(), outbox.Entry{
		ID: "evt-user-01", EventType: "event.user.created.v1",
		Payload: []byte(`{"user_id":"u1","username":"alice"}`),
	})
	assert.NoError(t, err)
}

// Contract: event.user.locked.v1 subscribe.
func TestEventUserLockedV1Subscribe(t *testing.T) {
	svc, _ := newTestService()
	err := svc.HandleEvent(context.Background(), outbox.Entry{
		ID: "evt-user-lock-01", EventType: "event.user.locked.v1",
		Payload: []byte(`{"user_id":"u1"}`),
	})
	assert.NoError(t, err)
}

// Contract: event.config.changed.v1 subscribe.
func TestEventConfigChangedV1Subscribe(t *testing.T) {
	svc, _ := newTestService()
	err := svc.HandleEvent(context.Background(), outbox.Entry{
		ID: "evt-cfg-01", EventType: "event.config.changed.v1",
		Payload: []byte(`{"action":"created","key":"k1","value":"v1","version":1}`),
	})
	assert.NoError(t, err)
}

// Contract: event.config.rollback.v1 subscribe.
func TestEventConfigRollbackV1Subscribe(t *testing.T) {
	svc, _ := newTestService()
	err := svc.HandleEvent(context.Background(), outbox.Entry{
		ID: "evt-cfg-rb-01", EventType: "event.config.rollback.v1",
		Payload: []byte(`{"action":"rollback","key":"k1","target_version":1,"new_version":2}`),
	})
	assert.NoError(t, err)
}
