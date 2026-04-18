package rbacassign

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- stubs ---

type stubOutboxWriter struct {
	entries []outbox.Entry
	err     error
}

func (s *stubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, e)
	return nil
}

type stubTxRunner struct {
	calls int
}

func (s *stubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(context.Background())
}

// trackingSessionRepo wraps mem.SessionRepository and counts RevokeByUserID calls.
type trackingSessionRepo struct {
	*mem.SessionRepository
	revokeCalls int
}

func (r *trackingSessionRepo) RevokeByUserID(ctx context.Context, userID string) error {
	r.revokeCalls++
	return r.SessionRepository.RevokeByUserID(ctx, userID)
}

// newDurableTestService creates a Service with outboxWriter + txRunner injected (durable mode).
func newDurableTestService(ow *stubOutboxWriter, tx *stubTxRunner) (*Service, *mem.RoleRepository, *trackingSessionRepo) {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	sessionRepo := &trackingSessionRepo{SessionRepository: mem.NewSessionRepository()}
	svc := NewService(roleRepo, sessionRepo, slog.Default(),
		WithOutboxWriter(ow),
		WithTxManager(tx),
	)
	return svc, roleRepo, sessionRepo
}

// TestService_Assign_Durable_WritesOutboxAtomically asserts that Assign in durable
// mode writes exactly one outbox entry with the correct EventType and payload, runs
// the operation inside a transaction, and does NOT call sessionRepo.RevokeByUserID
// (consumer handles that asynchronously).
func TestService_Assign_Durable_WritesOutboxAtomically(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, _, sessionRepo := newDurableTestService(ow, tx)

	err := svc.Assign(context.Background(), "alice", "admin")
	require.NoError(t, err)

	// Exactly one outbox entry.
	require.Len(t, ow.entries, 1)
	assert.Equal(t, TopicRoleAssigned, ow.entries[0].EventType)

	// Payload unmarshal.
	var evt RoleChangedEvent
	require.NoError(t, json.Unmarshal(ow.entries[0].Payload, &evt))
	assert.Equal(t, "alice", evt.UserID)
	assert.Equal(t, "admin", evt.RoleID)
	assert.Equal(t, ActionAssigned, evt.Action)

	// Transaction invoked exactly once.
	assert.Equal(t, 1, tx.calls)

	// sessionRepo.RevokeByUserID must NOT be called in durable mode — consumer takes over.
	assert.Equal(t, 0, sessionRepo.revokeCalls,
		"durable mode: sessionRepo.RevokeByUserID must not be called by rbacassign (consumer handles it)")
}

// TestService_Revoke_Durable_WritesOutboxAtomically is the symmetrical test for Revoke.
func TestService_Revoke_Durable_WritesOutboxAtomically(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, roleRepo, sessionRepo := newDurableTestService(ow, tx)

	// Need two admins so last-admin guard passes.
	_ = roleRepo.AssignToUser(context.Background(), "alice", "admin")
	_ = roleRepo.AssignToUser(context.Background(), "bob", "admin")

	err := svc.Revoke(context.Background(), "alice", "admin")
	require.NoError(t, err)

	require.Len(t, ow.entries, 1)
	assert.Equal(t, TopicRoleRevoked, ow.entries[0].EventType)

	var evt RoleChangedEvent
	require.NoError(t, json.Unmarshal(ow.entries[0].Payload, &evt))
	assert.Equal(t, "alice", evt.UserID)
	assert.Equal(t, "admin", evt.RoleID)
	assert.Equal(t, ActionRevoked, evt.Action)

	assert.Equal(t, 1, tx.calls)

	assert.Equal(t, 0, sessionRepo.revokeCalls,
		"durable mode: sessionRepo.RevokeByUserID must not be called by rbacassign (consumer handles it)")
}

// TestService_Durable_OutboxWriteFailure_PropagatesError asserts that when the outbox
// writer returns an error, RunInTx propagates it and the service returns a wrapped error.
// The actual DB rollback semantic is validated by PG integration tests in
// adapters/postgres/... — the in-memory stubTxRunner here does not model tx rollback
// of the role write. This test only covers error-propagation, not rollback.
func TestService_Durable_OutboxWriteFailure_PropagatesError(t *testing.T) {
	outboxErr := errors.New("outbox write failed")
	ow := &stubOutboxWriter{err: outboxErr}
	tx := &stubTxRunner{}
	svc, _, _ := newDurableTestService(ow, tx)

	err := svc.Assign(context.Background(), "alice", "admin")
	require.Error(t, err, "Assign must return error when outbox write fails")
	assert.ErrorIs(t, err, outboxErr, "original outbox error must be in the chain")

	// outboxWriter.entries is empty because Write returned error before appending.
	assert.Empty(t, ow.entries)
}

// TestService_Durable_DoesNotCallSessionRepoDirectly asserts that in durable mode
// sessionRepo.RevokeByUserID is never called by Assign or Revoke (counter must remain 0).
func TestService_Durable_DoesNotCallSessionRepoDirectly(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, roleRepo, sessionRepo := newDurableTestService(ow, tx)

	// Assign.
	require.NoError(t, svc.Assign(context.Background(), "u1", "admin"))

	// Revoke — needs two admins to pass last-admin guard.
	_ = roleRepo.AssignToUser(context.Background(), "u2", "admin")
	require.NoError(t, svc.Revoke(context.Background(), "u1", "admin"))

	assert.Equal(t, 0, sessionRepo.revokeCalls,
		"durable mode: sessionRepo.RevokeByUserID must never be called directly")
}
