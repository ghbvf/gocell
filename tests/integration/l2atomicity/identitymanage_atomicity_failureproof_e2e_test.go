//go:build integration

package l2atomicity

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// Topic literals are kept inlined (not imported from
// corecells/accesscore/internal/dto) because the tests/integration/l2atomicity
// package cannot import internal/. Same "expected duplication" rationale as
// eventRoleAssignedV1 in rbacassign_atomicity_failureproof_e2e_test.go: if the
// producer-side constant changes, this test must be updated in lockstep — and
// will visibly fail in the inject-failure window when the new event type fails
// to be intercepted. The negative-control path clears failType and is
// topic-agnostic by design.
const (
	eventUserCreatedV1 = "event.user.created.v1"
)

// ---------------------------------------------------------------------------
// TestL2Atomicity_identitymanage_RollsBack (L2-OUTBOX-ATOMICITY-COVERAGE-01)
// ---------------------------------------------------------------------------

// TestL2Atomicity_identitymanage_RollsBack proves that when the outbox writer
// fails inside identitymanage.Service's transaction (user creation path), the
// domain write (users INSERT) rolls back atomically and no user row persists.
//
// Setup phase uses a pass-through writer so admin login succeeds normally.
// The failure window is opened only for event.user.created.v1 immediately
// before the create-user-under-test call.
//
// ref: rbacassign_atomicity_failureproof_e2e_test.go (canonical pattern)
// ref: Transactional Outbox Pattern (Microservices Patterns, Chris Richardson)
func TestL2Atomicity_identitymanage_RollsBack(t *testing.T) {
	sw := newSelectiveFailWriter(adapterpg.NewOutboxWriter(clock.Real()))
	h := newL2HarnessWithWriter(t, sw)
	ctx := context.Background()

	// Setup phase: pass-through writer. Admin login passes through.
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)

	const newUsername = "l2-identitymanage-atomicity-user"
	require.Equal(t, 0, userCountByUsername(t, h, newUsername),
		"baseline: target username must not exist before create-under-test")

	auditBefore := countAuditEntries(t, ctx, h, eventUserCreatedV1)

	// Trigger window: only event.user.created.v1 will fail at the writer.
	sw.failType.Store(eventUserCreatedV1)

	// The create-user-under-test: admin creates a new user. The outbox writer
	// will fail when identitymanage.Service tries to emit event.user.created.v1
	// inside the transaction, causing the txRunner to roll back the users INSERT.
	status := httpCreateUserStatus(t, h.base, adminLogin.AccessToken,
		newUsername, "identitymanage-atomicity@l2.local", victimPassword)
	require.Equal(t, http.StatusInternalServerError, status,
		"user creation must surface 500 when outbox writer fails")

	require.Equal(t, int64(1), sw.failCount.Load(),
		"writer.Write must have been invoked exactly once before transaction rollback")

	// Rollback proof: users INSERT did NOT persist.
	require.Equal(t, 0, userCountByUsername(t, h, newUsername),
		"users row MUST NOT persist after outbox-write failure rollback")

	// Audit-silence proof: no audit entry must appear for the aborted user creation.
	require.Equal(t, auditBefore, countAuditEntries(t, ctx, h, eventUserCreatedV1),
		"rollback: no audit entry must appear for aborted user creation")

	// Negative control: with the trigger cleared, the same create path must
	// succeed and a user row must appear.
	sw.failType.Store("")
	httpCreateUser(t, h.base, adminLogin.AccessToken,
		newUsername, "identitymanage-atomicity@l2.local", victimPassword)
	assert.Equal(t, 1, userCountByUsername(t, h, newUsername),
		"control: users row MUST persist on happy user creation")
}
