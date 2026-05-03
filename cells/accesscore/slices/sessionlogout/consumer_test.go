package sessionlogout

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// --- stubs ---

// trackingSessionRepo counts RevokeByUserID calls and delegates to ports.SessionRepository.
type trackingSessionRepo struct {
	ports.SessionRepository
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

func validPayload(userID string) []byte {
	return []byte(`{"userId":"` + userID + `","roleId":"admin","action":"revoked"}`)
}

func makeEntry(id string, payload []byte) outbox.Entry {
	return outbox.Entry{
		ID:        id,
		EventType: "event.role.revoked.v1",
		Payload:   payload,
	}
}

// --- consumer tests ---

func TestHandleRoleChanged_Ack(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-abc", validPayload("u1"))
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
	assert.Equal(t, 1, repo.revokeCalls, "RevokeByUserID must be called exactly once")
}

func TestHandleRoleChanged_PermErrReject_MalformedPayload(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-bad", []byte("not-json"))
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.Error(t, result.Err)
	var permErr *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &permErr), "malformed payload must return PermanentError")
}

func TestHandleRoleChanged_PermErrReject_EmptyUserID(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-empty", validPayload(""))
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.Error(t, result.Err)
	var permErr *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &permErr), "empty userId must return PermanentError")
}

func TestHandleRoleChanged_RepoErrRequeue(t *testing.T) {
	dbErr := errors.New("db down")
	repo := &errorSessionRepo{err: dbErr}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-transient", validPayload("u1"))
	result := c.HandleRoleChanged(context.Background(), entry)

	assert.Equal(t, outbox.DispositionRequeue, result.Disposition)
	require.Error(t, result.Err)
	// Must NOT be a PermanentError — transient errors trigger Requeue.
	var permErr *outbox.PermanentError
	assert.False(t, errors.As(result.Err, &permErr), "transient DB error must NOT be PermanentError")
	assert.ErrorIs(t, result.Err, dbErr)
}

// TestHandleRoleChanged_ReplayIdempotent_SecondCallSafe verifies that calling the
// handler twice with the same entry ID is safe. The handler itself is naturally idempotent
// because RevokeByUserID is idempotent (revoking already-revoked sessions is a no-op).
// Infrastructure-level idempotency (Claimer dedup) is provided by ConsumerBase and is NOT
// tested here — this test documents the handler's own idempotency contract.
func TestHandleRoleChanged_ReplayIdempotent_SecondCallSafe(t *testing.T) {
	repo := &trackingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	c := NewConsumer(repo, slog.Default())

	entry := makeEntry("evt-replay", validPayload("u1"))

	// First call.
	result1 := c.HandleRoleChanged(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result1.Disposition)

	// Second call — must also Ack (idempotent).
	result2 := c.HandleRoleChanged(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result2.Disposition)

	// sessionRepo.RevokeByUserID is called twice — both are safe because the operation is idempotent.
	assert.Equal(t, 2, repo.revokeCalls)
}
