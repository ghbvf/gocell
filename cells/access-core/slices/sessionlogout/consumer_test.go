package sessionlogout

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- stubs ---

// trackingSessionRepo counts RevokeByUserID calls and delegates to mem.SessionRepository.
type trackingSessionRepo struct {
	*mem.SessionRepository
	revokeCalls int
}

func (r *trackingSessionRepo) RevokeByUserID(ctx context.Context, userID string) error {
	r.revokeCalls++
	return r.SessionRepository.RevokeByUserID(ctx, userID)
}

// errorSessionRepo returns a configurable error from RevokeByUserID.
type errorSessionRepo struct {
	ports.SessionRepository
	err error
}

func (r *errorSessionRepo) RevokeByUserID(_ context.Context, _ string) error {
	return r.err
}

// --- helpers ---

func validPayload(userID, roleID, action string) []byte {
	return []byte(`{"userId":"` + userID + `","roleId":"` + roleID + `","action":"` + action + `"}`)
}

func makeEntry(id string, payload []byte) outbox.Entry {
	return outbox.Entry{
		ID:        id,
		EventType: "event.role.revoked.v1",
		Payload:   payload,
	}
}

// --- consumer tests ---

func TestConsumer_HandleRoleChanged_HappyPath_ReturnsNil_CallsRevokeByUserID(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: mem.NewSessionRepository()}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-abc", validPayload("u1", "admin", "revoked"))
	err := c.HandleRoleChanged(context.Background(), entry)

	require.NoError(t, err)
	assert.Equal(t, 1, repo.revokeCalls, "RevokeByUserID must be called exactly once")
}

func TestConsumer_HandleRoleChanged_MalformedPayload_ReturnsPermanentError(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: mem.NewSessionRepository()}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-bad", []byte("not-json"))
	err := c.HandleRoleChanged(context.Background(), entry)

	require.Error(t, err)
	var permErr *outbox.PermanentError
	assert.True(t, errors.As(err, &permErr), "malformed payload must return PermanentError")
}

func TestConsumer_HandleRoleChanged_EmptyUserID_ReturnsPermanentError(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: mem.NewSessionRepository()}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-empty", validPayload("", "admin", "revoked"))
	err := c.HandleRoleChanged(context.Background(), entry)

	require.Error(t, err)
	var permErr *outbox.PermanentError
	assert.True(t, errors.As(err, &permErr), "empty userId must return PermanentError")
}

func TestConsumer_HandleRoleChanged_TransientRepoError_ReturnsPlainError(t *testing.T) {
	dbErr := errors.New("db down")
	repo := &errorSessionRepo{err: dbErr}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-transient", validPayload("u1", "admin", "revoked"))
	err := c.HandleRoleChanged(context.Background(), entry)

	require.Error(t, err)
	// Must NOT be a PermanentError (WrapLegacyHandler maps to Requeue).
	var permErr *outbox.PermanentError
	assert.False(t, errors.As(err, &permErr), "transient DB error must NOT be PermanentError")
	assert.ErrorIs(t, err, dbErr)
}

// TestConsumer_HandleRoleChanged_ReplayIdempotent_SecondCallSafe verifies that calling the
// handler twice with the same entry ID is safe. The handler itself is naturally idempotent
// because RevokeByUserID is idempotent (revoking already-revoked sessions is a no-op).
// Infrastructure-level idempotency (Claimer dedup) is provided by ConsumerBase and is NOT
// tested here — this test documents the handler's own idempotency contract.
func TestConsumer_HandleRoleChanged_ReplayIdempotent_SecondCallSafe(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: mem.NewSessionRepository()}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-replay", validPayload("u1", "admin", "revoked"))

	// First call.
	require.NoError(t, c.HandleRoleChanged(context.Background(), entry))
	// Second call — must also return nil (idempotent).
	require.NoError(t, c.HandleRoleChanged(context.Background(), entry))

	// sessionRepo.RevokeByUserID is called twice — both are safe because the operation is idempotent.
	assert.Equal(t, 2, repo.revokeCalls)
}

// --- WrapLegacyHandler disposition tests ---

func TestConsumer_ViaWrapLegacyHandler_PermanentErrorMapsToReject(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: mem.NewSessionRepository()}
	c := NewConsumer(repo, slog.Default())
	handler := outbox.WrapLegacyHandler(c.HandleRoleChanged)

	entry := makeEntry("evt-perm", []byte("not-json"))
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.NotNil(t, result.Err)
}

func TestConsumer_ViaWrapLegacyHandler_HappyPathMapsToAck(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: mem.NewSessionRepository()}
	c := NewConsumer(repo, slog.Default())
	handler := outbox.WrapLegacyHandler(c.HandleRoleChanged)

	entry := makeEntry("evt-ack", validPayload("u1", "admin", "revoked"))
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.Nil(t, result.Err)
}

func TestConsumer_ViaWrapLegacyHandler_TransientMapsToRequeue(t *testing.T) {
	dbErr := errors.New("transient db error")
	repo := &errorSessionRepo{err: dbErr}
	c := NewConsumer(repo, slog.Default())
	handler := outbox.WrapLegacyHandler(c.HandleRoleChanged)

	entry := makeEntry("evt-requeue", validPayload("u1", "admin", "revoked"))
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionRequeue, result.Disposition)
	require.NotNil(t, result.Err)
}
