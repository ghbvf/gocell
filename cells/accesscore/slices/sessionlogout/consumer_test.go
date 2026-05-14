package sessionlogout

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// --- helpers ---

func validRevokedPayload(userID string) []byte {
	return []byte(`{"userId":"` + userID + `","roleId":"admin","action":"` + dto.ActionRevoked + `"}`)
}

func validAssignedPayload(userID string) []byte {
	return []byte(`{"userId":"` + userID + `","roleId":"admin","action":"` + dto.ActionAssigned + `"}`)
}

func makeEntry(id string, payload []byte) outbox.Entry {
	return outbox.Entry{
		ID:        id,
		EventType: "event.role.revoked.v1",
		Payload:   payload,
	}
}

// --- consumer tests ---

// TestHandleRoleChanged_Revoked_Ack asserts that a well-formed revoked event
// Acks without error. Credential invalidation is performed by rbacassign in the
// same transaction; this consumer only processes the downstream outbox fact.
func TestHandleRoleChanged_Revoked_Ack(t *testing.T) {
	c := NewConsumer(slog.Default())

	entry := makeEntry("evt-revoked", validRevokedPayload("u1"))
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

// TestHandleRoleChanged_Assigned_Ack asserts that a well-formed assigned event
// also Acks without error (HIGH-3: assignment is additive, no credential invalidation).
func TestHandleRoleChanged_Assigned_Ack(t *testing.T) {
	c := NewConsumer(slog.Default())

	entry := makeEntry("evt-assigned", validAssignedPayload("u2"))
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

// TestHandleRoleChanged_DoesNotCallRevokeForSubject verifies that the consumer
// does not attempt to call session.Store.RevokeForSubject — credential
// invalidation is done by rbacassign.Revoke in the same transaction.
// This test is structural: the Consumer struct has no sessionStore field,
// so the test simply confirms the consumer can handle a revoked event
// without any session store configured.
func TestHandleRoleChanged_DoesNotCallRevokeForSubject(t *testing.T) {
	c := NewConsumer(slog.Default())
	// If the consumer tried to call RevokeForSubject it would panic/NPE since
	// there is no sessionStore — the fact that this returns Ack proves it doesn't.
	entry := makeEntry("evt-no-revoke", validRevokedPayload("u3"))
	result := c.HandleRoleChanged(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
}

// TestHandleRoleChanged_UnknownAction_RejectsPermanent asserts that an event
// with an unknown action value is Rejected with a PermanentError to prevent
// silent data loss on future protocol extensions.
func TestHandleRoleChanged_UnknownAction_RejectsPermanent(t *testing.T) {
	c := NewConsumer(slog.Default())

	payload := []byte(`{"userId":"u4","roleId":"admin","action":"suspended"}`)
	entry := makeEntry("evt-unknown-action", payload)
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.Error(t, result.Err)
	var permErr *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &permErr),
		"unknown action must return PermanentError to route to DLX")
}

func TestHandleRoleChanged_PermErrReject_MalformedPayload(t *testing.T) {
	c := NewConsumer(slog.Default())

	entry := makeEntry("evt-bad", []byte("not-json"))
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.Error(t, result.Err)
	var permErr *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &permErr), "malformed payload must return PermanentError")
}

func TestHandleRoleChanged_PermErrReject_EmptyUserID(t *testing.T) {
	c := NewConsumer(slog.Default())

	entry := makeEntry("evt-empty", validRevokedPayload(""))
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.Error(t, result.Err)
	var permErr *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &permErr), "empty userId must return PermanentError")
}

// TestHandleRoleChanged_ReplayIdempotent_SecondCallSafe verifies that calling the
// handler twice with the same entry ID is safe. Since the handler performs no
// side effects (credential invalidation was already done in rbacassign), replay
// is naturally idempotent.
func TestHandleRoleChanged_ReplayIdempotent_SecondCallSafe(t *testing.T) {
	c := NewConsumer(slog.Default())

	entry := makeEntry("evt-replay", validRevokedPayload("u5"))

	result1 := c.HandleRoleChanged(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result1.Disposition)

	result2 := c.HandleRoleChanged(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result2.Disposition)
}
