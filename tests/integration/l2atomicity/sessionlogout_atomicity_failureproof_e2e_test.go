//go:build integration

package l2atomicity

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
)

// Topic literals are kept inlined (not imported from
// cells/accesscore/internal/dto) because the tests/integration/l2atomicity
// package cannot import internal/. Same "expected duplication" rationale as
// eventRoleAssignedV1 in rbacassign_atomicity_failureproof_e2e_test.go: if the
// producer-side constant changes, this test must be updated in lockstep — and
// will visibly fail in the inject-failure window when the new event type fails
// to be intercepted. The negative-control path clears failType and is
// topic-agnostic by design.
const (
	eventSessionRevokedV1 = "event.session.revoked.v1"
)

// ---------------------------------------------------------------------------
// TestL2Atomicity_sessionlogout_RollsBack (L2-OUTBOX-ATOMICITY-COVERAGE-01)
// ---------------------------------------------------------------------------

// TestL2Atomicity_sessionlogout_RollsBack proves that when the outbox writer
// fails inside sessionlogout.Service's transaction, the domain write
// (sessions revoked_at UPDATE) rolls back atomically and the session
// remains live.
//
// Setup phase uses a pass-through writer so admin login, victim creation, and
// victim's initial login all succeed normally. The failure window is opened
// only for event.session.revoked.v1 immediately before the logout-under-test.
//
// ref: rbacassign_atomicity_failureproof_e2e_test.go (canonical pattern)
// ref: Transactional Outbox Pattern (Microservices Patterns, Chris Richardson)
func TestL2Atomicity_sessionlogout_RollsBack(t *testing.T) {
	sw := newSelectiveFailWriter(adapterpg.NewOutboxWriter(clock.Real()))
	h := newL2HarnessWithWriter(t, sw)
	ctx := context.Background()

	// Setup phase: pass-through writer. Admin login, victim user creation,
	// and victim login all pass through (failType is "").
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	const victimUsername = "l2-sessionlogout-atomicity-user"
	const victimLogoutPassword = "VictimPass!99"
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken,
		victimUsername, "sessionlogout-atomicity@l2.local", victimLogoutPassword)

	// Login the victim so there is a live session to revoke.
	victimLogin := httpLogin(t, h.base, victimUsername, victimLogoutPassword)
	require.NotEmpty(t, victimLogin.SessionID, "setup: victim must have a live session")

	require.Equal(t, 1, countLiveSessions(t, h, victimID),
		"setup: victim must have exactly 1 live session before logout-under-test")

	auditBefore := countAuditEntries(t, ctx, h, eventSessionRevokedV1)

	// Trigger window: only event.session.revoked.v1 will fail at the writer.
	sw.failType.Store(eventSessionRevokedV1)

	// The logout-under-test: victim logs out. The outbox writer will fail when
	// sessionlogout.Service tries to emit event.session.revoked.v1 inside the
	// transaction, causing the txRunner to roll back the revoked_at UPDATE.
	status := httpLogoutStatus(t, h.base, victimLogin.AccessToken, victimLogin.SessionID)
	require.Equal(t, http.StatusInternalServerError, status,
		"logout must surface 500 when outbox writer fails")

	require.Equal(t, int64(1), sw.failCount.Load(),
		"writer.Write must have been invoked exactly once before transaction rollback")

	// Rollback proof: session revoked_at UPDATE did NOT persist — session is
	// still live.
	require.Equal(t, 1, countLiveSessions(t, h, victimID),
		"sessions row MUST remain live after outbox-write failure rollback")

	// Audit-silence proof: no audit entry must appear for the aborted logout.
	require.Equal(t, auditBefore, countAuditEntries(t, ctx, h, eventSessionRevokedV1),
		"rollback: no audit entry must appear for aborted logout")

	// Negative control: with the trigger cleared, the same logout path must
	// succeed and the session must be revoked.
	sw.failType.Store("")
	httpLogout(t, h.base, victimLogin.AccessToken, victimLogin.SessionID)
	assert.Equal(t, 0, countLiveSessions(t, h, victimID),
		"control: session MUST be revoked on happy logout")
}
