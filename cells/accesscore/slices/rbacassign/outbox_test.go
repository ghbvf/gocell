package rbacassign

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/persistence"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth/session"
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

// trackingSessionStore wraps session.Store and counts RevokeForSubject calls.
type trackingSessionStore struct {
	session.Store
	revokeCalls int
}

func (r *trackingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	r.revokeCalls++
	return r.Store.RevokeForSubject(ctx, subjectID, event)
}

// newDurableTestService creates a Service with emitter + txRunner injected (durable mode).
// Returns the shared mem.Store so callers can seed active users for effective-admin
// invariant tests (S4.0).
func newDurableTestService(t testing.TB, ow *stubOutboxWriter, tx *stubTxRunner) (*Service, *mem.Store, *trackingSessionStore) {
	t.Helper()
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	sessionStore := &trackingSessionStore{Store: testutil.RealSessionRepo(t)}
	svc := mustNewService(t, store.RoleRepository(), sessionStore, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)),
		WithTxManager(persistence.WrapForCell(tx)),
	)
	return svc, store, sessionStore
}

// TestService_Assign_Durable_WritesOutboxAtomically asserts that Assign in durable
// mode writes exactly one outbox entry with the correct EventType and payload, runs
// the operation inside a transaction, and does NOT call sessionStore.RevokeForSubject
// (consumer handles that asynchronously).
func TestService_Assign_Durable_WritesOutboxAtomically(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, _, sessionStore := newDurableTestService(t, ow, tx)

	err := svc.Assign(context.Background(), "alice", "admin")
	require.NoError(t, err)

	// Exactly one outbox entry.
	require.Len(t, ow.entries, 1)
	assert.Equal(t, dto.TopicRoleAssigned, ow.entries[0].EventType)

	// Payload unmarshal.
	var evt dto.RoleChangedEvent
	require.NoError(t, json.Unmarshal(ow.entries[0].Payload, &evt))
	assert.Equal(t, "alice", evt.UserID)
	assert.Equal(t, "admin", evt.RoleID)
	assert.Equal(t, dto.ActionAssigned, evt.Action)

	// Transaction invoked exactly once.
	assert.Equal(t, 1, tx.calls)

	// sessionStore.RevokeForSubject must NOT be called in durable mode — consumer takes over.
	assert.Equal(t, 0, sessionStore.revokeCalls,
		"durable mode: sessionStore.RevokeForSubject must not be called by rbacassign (consumer handles it)")
}

// TestService_Revoke_Durable_WritesOutboxAtomically is the symmetrical test for Revoke.
func TestService_Revoke_Durable_WritesOutboxAtomically(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, store, sessionStore := newDurableTestService(t, ow, tx)

	// Need two effective (active) admins so the effective-admin guard passes.
	assignActiveAdmin(t, store, "alice")
	assignActiveAdmin(t, store, "bob")

	err := svc.Revoke(context.Background(), "alice", "admin")
	require.NoError(t, err)

	require.Len(t, ow.entries, 1)
	assert.Equal(t, dto.TopicRoleRevoked, ow.entries[0].EventType)

	var evt dto.RoleChangedEvent
	require.NoError(t, json.Unmarshal(ow.entries[0].Payload, &evt))
	assert.Equal(t, "alice", evt.UserID)
	assert.Equal(t, "admin", evt.RoleID)
	assert.Equal(t, dto.ActionRevoked, evt.Action)

	assert.Equal(t, 1, tx.calls)

	assert.Equal(t, 0, sessionStore.revokeCalls,
		"durable mode: sessionStore.RevokeForSubject must not be called by rbacassign (consumer handles it)")
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
	svc, _, _ := newDurableTestService(t, ow, tx)

	err := svc.Assign(context.Background(), "alice", "admin")
	require.Error(t, err, "Assign must return error when outbox write fails")
	assert.ErrorIs(t, err, outboxErr, "original outbox error must be in the chain")

	// outbox entries are empty because Write returned error before appending.
	assert.Empty(t, ow.entries)
}

// TestService_Durable_DoesNotCallSessionStoreDirectly asserts that in durable mode
// sessionStore.RevokeForSubject is never called by Assign or Revoke (counter must remain 0).
func TestService_Durable_DoesNotCallSessionStoreDirectly(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, store, sessionStore := newDurableTestService(t, ow, tx)

	// Seed u1 as an effective admin (active + admin role) so Assign re-assigns
	// the existing holder idempotently and Revoke later has a real role row.
	assignActiveAdmin(t, store, "u1")
	// Idempotent re-assign for the Assign step (no-op since u1 already holds admin).
	require.NoError(t, svc.Assign(context.Background(), "u1", "admin"))

	// Revoke — needs a second effective admin to pass the guard.
	assignActiveAdmin(t, store, "u2")
	require.NoError(t, svc.Revoke(context.Background(), "u1", "admin"))

	assert.Equal(t, 0, sessionStore.revokeCalls,
		"durable mode: sessionStore.RevokeForSubject must never be called directly")
}

// TestService_Assign_Durable_RepeatIsNoop asserts that re-assigning a role the
// user already holds publishes no second outbox entry and writes no second
// event.role.assigned.v1 fact. Prevents a false positive in downstream
// consumers (e.g., audit log duplicates, unnecessary session invalidation).
func TestService_Assign_Durable_RepeatIsNoop(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, _, sessionStore := newDurableTestService(t, ow, tx)

	require.NoError(t, svc.Assign(context.Background(), "alice", "admin"))
	require.Len(t, ow.entries, 1, "first assign must publish exactly one event")

	require.NoError(t, svc.Assign(context.Background(), "alice", "admin"))
	assert.Len(t, ow.entries, 1, "repeat assign must not publish a second event")
	assert.Equal(t, 0, sessionStore.revokeCalls,
		"durable mode repeat assign must not call session revoke either")
}

// TestService_Revoke_Durable_NonMemberIsNoop asserts that revoking a role the
// user does not hold publishes no outbox entry (no event.role.revoked.v1 fact).
// Without this guard, a bogus revoke RPC would trigger downstream audit noise
// and unnecessary session invalidation for a user whose sessions should not
// be affected.
func TestService_Revoke_Durable_NonMemberIsNoop(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, store, sessionStore := newDurableTestService(t, ow, tx)

	// Seed two effective admin holders so the no-op revoke path is exercised
	// without falsely tripping the last-admin guard (alice does not hold
	// admin and the user record need not exist for a no-op revoke).
	assignActiveAdmin(t, store, "bob")
	assignActiveAdmin(t, store, "carol")

	require.NoError(t, svc.Revoke(context.Background(), "alice", "admin"))
	assert.Empty(t, ow.entries, "revoke of non-member must not publish any event")
	assert.Equal(t, 0, sessionStore.revokeCalls,
		"durable mode revoke of non-member must not call session revoke")
}

// TestService_Assign_Demo_RepeatIsNoop mirrors the durable no-op test for
// demo/synchronous dual-write mode: repeat assign must NOT call sessionStore.RevokeForSubject.
func TestService_Assign_Demo_RepeatIsNoop(t *testing.T) {
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	sessionStore := &trackingSessionStore{Store: testutil.RealSessionRepo(t)}
	svc := mustNewService(t, roleRepo, sessionStore, slog.Default())

	require.NoError(t, svc.Assign(context.Background(), "alice", "admin"))
	assert.Equal(t, 1, sessionStore.revokeCalls, "first assign must revoke sessions once")

	require.NoError(t, svc.Assign(context.Background(), "alice", "admin"))
	assert.Equal(t, 1, sessionStore.revokeCalls,
		"repeat assign must not trigger a second session revoke")
}
