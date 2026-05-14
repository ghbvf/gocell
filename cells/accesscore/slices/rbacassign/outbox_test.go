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

// newDurableTestService creates a Service with emitter + txRunner injected.
// The invalidator is backed by the tracking session store so tests can observe
// RevokeForSubject calls through the funnel.
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
	svc := mustNewService(t, store.RoleRepository(), store.UserRepository(), sessionStore, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)),
		WithTxManager(persistence.WrapForCell(tx)),
	)
	return svc, store, sessionStore
}

// TestService_Assign_Durable_WritesOutboxAtomically asserts that Assign writes
// exactly one outbox entry with the correct EventType and payload, runs the
// operation inside a transaction, and does NOT call sessionStore.RevokeForSubject
// (HIGH-3: Assign is additive; the funnel is not called).
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

	// HIGH-3: sessionStore.RevokeForSubject must NOT be called for Assign.
	assert.Equal(t, 0, sessionStore.revokeCalls,
		"HIGH-3: Assign is additive — funnel must not be called")
}

// TestService_Revoke_Durable_WritesOutboxAtomically asserts that Revoke writes
// exactly one outbox entry, runs inside a transaction, and calls the credential
// invalidation funnel (which calls sessionStore.RevokeForSubject) atomically.
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

	// Funnel calls RevokeForSubject inside the same tx.
	assert.Equal(t, 1, sessionStore.revokeCalls,
		"Revoke: credential invalidation funnel must call RevokeForSubject once")
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

// TestService_Assign_DoesNotCallFunnel asserts that Assign never calls
// sessionStore.RevokeForSubject (HIGH-3: Assign is additive, funnel not called).
// Revoke DOES call the funnel, so this test uses only Assign.
func TestService_Assign_DoesNotCallFunnel(t *testing.T) {
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, _, sessionStore := newDurableTestService(t, ow, tx)

	require.NoError(t, svc.Assign(context.Background(), "u1", "admin"))

	assert.Equal(t, 0, sessionStore.revokeCalls,
		"HIGH-3: Assign must never call the credential invalidation funnel")
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

// TestService_Assign_RepeatIsNoop_NeverCallsFunnel asserts that Assign is always
// a no-op with respect to credential invalidation (HIGH-3): neither first nor
// repeat Assign calls sessionStore.RevokeForSubject.
func TestService_Assign_RepeatIsNoop_NeverCallsFunnel(t *testing.T) {
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	sessionStore := &trackingSessionStore{Store: testutil.RealSessionRepo(t)}
	svc := mustNewService(t, store.RoleRepository(), store.UserRepository(), sessionStore, slog.Default())

	require.NoError(t, svc.Assign(context.Background(), "alice", "admin"))
	assert.Equal(t, 0, sessionStore.revokeCalls,
		"HIGH-3: Assign must never call the credential funnel (first call)")

	require.NoError(t, svc.Assign(context.Background(), "alice", "admin"))
	assert.Equal(t, 0, sessionStore.revokeCalls,
		"HIGH-3: Assign must never call the credential funnel (repeat call)")
}
