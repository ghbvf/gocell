//go:build integration

package l2atomicity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	eventSessionCreatedV1 = "event.session.created.v1"
)

// ---------------------------------------------------------------------------
// TestL2Atomicity_sessionlogin_RollsBack (L2-OUTBOX-ATOMICITY-COVERAGE-01)
// ---------------------------------------------------------------------------

// TestL2Atomicity_sessionlogin_RollsBack proves that when the outbox writer
// fails inside sessionlogin.Service's transaction, the domain write
// (sessions INSERT) rolls back atomically and no session row persists.
//
// Pattern mirrors TestL2Atomicity_rbacassign_RollsBack: setup phase uses a
// pass-through writer so admin login and victim user creation succeed normally;
// the failure window is opened only for event.session.created.v1 immediately
// before the login-under-test.
//
// ref: rbacassign_atomicity_failureproof_e2e_test.go (canonical pattern)
// ref: Transactional Outbox Pattern (Microservices Patterns, Chris Richardson)
func TestL2Atomicity_sessionlogin_RollsBack(t *testing.T) {
	sw := newSelectiveFailWriter(adapterpg.NewOutboxWriter(clock.Real()))
	h := newL2HarnessWithWriter(t, sw)
	ctx := context.Background()

	// Setup phase: pass-through writer. Admin login and victim user creation
	// both pass through (failType is "").
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	const victimUsername = "l2-sessionlogin-atomicity-user"
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken,
		victimUsername, "sessionlogin-atomicity@l2.local", victimPassword)

	require.Equal(t, 0, countLiveSessions(t, h, victimID),
		"baseline: victim must have no live sessions before login-under-test")

	auditBefore := countAuditEntries(t, ctx, h, eventSessionCreatedV1)

	// Trigger window: only event.session.created.v1 will fail at the writer.
	sw.failType.Store(eventSessionCreatedV1)

	// The login-under-test: victim logs in. The outbox writer will fail when
	// sessionlogin.Service tries to emit event.session.created.v1 inside the
	// transaction, causing the txRunner to roll back the sessions INSERT.
	loginBody, _ := json.Marshal(map[string]string{
		"username": victimUsername,
		"password": victimPassword,
	})
	req, _ := http.NewRequest(http.MethodPost, h.base+"/api/v1/access/sessions/login",
		bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", l2TestTenantID)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"login must surface 500 when outbox writer fails")

	require.Equal(t, int64(1), sw.failCount.Load(),
		"writer.Write must have been invoked exactly once before transaction rollback")

	// Rollback proof: sessions INSERT did NOT persist.
	require.Equal(t, 0, countLiveSessions(t, h, victimID),
		"sessions row MUST NOT persist after outbox-write failure rollback")

	// Audit-silence proof: no audit entry must appear for the aborted login.
	require.Equal(t, auditBefore, countAuditEntries(t, ctx, h, eventSessionCreatedV1),
		"rollback: no audit entry must appear for aborted login")

	// Negative control: with the trigger cleared, the same login path must
	// succeed and a session row must appear.
	sw.failType.Store("")
	victimLogin := httpLogin(t, h.base, victimUsername, victimPassword)
	assert.Equal(t, 1, countLiveSessions(t, h, victimID),
		"control: sessions row MUST persist on happy login")
	assert.NotEmpty(t, victimLogin.SessionID, "control: sessionId must be present")
}
