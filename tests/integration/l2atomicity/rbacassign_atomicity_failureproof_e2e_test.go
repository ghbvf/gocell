//go:build integration

package l2atomicity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Topic literals are kept inlined (not imported from
// cells/accesscore/internal/dto) because the tests/integration/l2atomicity
// package cannot import internal/. Same "expected duplication" rationale as
// eventRoleRevokedV1 in revoke_cascade_e2e_test.go: if the producer-side
// constant changes, this test must be updated in lockstep — and will visibly
// fail when the new event type fails to be intercepted by selectiveFailWriter.
const (
	topicRoleAssignedV1 = "event.role.assigned.v1"
	topicRoleRevokedV1  = "event.role.revoked.v1"
)

// simulatedOutboxErr is the sentinel returned by selectiveFailWriter when it
// chooses to fail. The contents are observable only in slog (the framework
// span recorder redacts before reaching the wire); we only assert that the
// HTTP layer surfaces a 5xx envelope.
var simulatedOutboxErr = errors.New("simulated outbox write failure (RBACASSIGN-L2-PG-ATOMICITY-01)")

// selectiveFailWriter wraps a real outbox.Writer and returns simulatedOutboxErr
// when an entry's EventType matches failType.Load().(string). Atomic store
// allows tests to flip behavior mid-run without rebuilding the harness:
// "setup phase passes through → trigger window fails on specific event class".
//
// The inner writer is the real adapterpg.NewOutboxWriter so the pass-through
// path exercises full PG outbox semantics; only the fail path short-circuits.
type selectiveFailWriter struct {
	inner    outbox.Writer
	failType atomic.Value // string
}

func (w *selectiveFailWriter) Write(ctx context.Context, entry outbox.Entry) error {
	if t, _ := w.failType.Load().(string); t != "" && entry.EventType == t {
		return simulatedOutboxErr
	}
	return w.inner.Write(ctx, entry)
}

// newSelectiveFailWriter constructs a selectiveFailWriter that delegates to a
// fresh adapterpg.NewOutboxWriter and starts in pass-through mode.
func newSelectiveFailWriter() *selectiveFailWriter {
	w := &selectiveFailWriter{inner: adapterpg.NewOutboxWriter(clock.Real())}
	w.failType.Store("")
	return w
}

// roleAssignmentCount returns the number of role_assignments rows for
// (userID, roleID). Inlined rather than added to helpers_test.go so the
// helper remains scoped to the only test that needs it; if a future test
// adopts the same query, hoist then.
func roleAssignmentCount(t *testing.T, h *l2Harness, userID, roleID string) int {
	t.Helper()
	var n int
	err := h.pool.DB().QueryRow(context.Background(),
		`SELECT count(*) FROM role_assignments WHERE user_id = $1 AND role_id = $2`,
		userID, roleID).Scan(&n)
	require.NoError(t, err)
	return n
}

// postRoleEndpoint posts to /internal/v1/access/roles/{action} with a service
// token signed for "accesscore" and returns the status code. Inlined rather
// than parameterized into assignRole/revokeRole because the failure path
// requires a non-201/200 response which the existing helpers reject via
// require.Equal — modifying the helpers would loosen guarantees relied on by
// happy-path tests.
//
// Caller-cell literal "accesscore" is required by SVCTOKEN-CALLER-CELL-REQUIRED-01
// archtest (string literal at GenerateServiceToken callsite).
func postRoleEndpoint(t *testing.T, h *l2Harness, action, userID, roleID string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"userId": userID, "roleId": roleID})
	path := "/internal/v1/access/roles/" + action
	token := auth.GenerateServiceToken(h.ring, "accesscore", http.MethodPost, path, "", time.Now())
	req, _ := http.NewRequest(http.MethodPost, h.internalBase+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "ServiceToken "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// ---------------------------------------------------------------------------
// TestL2_RbacAssign_OutboxWriteFailure_RollsBack (RBACASSIGN-L2-PG-ATOMICITY-01)
// ---------------------------------------------------------------------------

// TestL2_RbacAssign_OutboxWriteFailure_RollsBack proves that when the outbox
// writer fails inside rbacassign.Service.Assign's transaction, the domain
// write (role_assignments INSERT via PGRoleRepo.AssignToUser) rolls back
// atomically and no row persists.
//
// Mirrors TestAuditLedgerStore_OutboxAtomicityFailureProof
// (adapters/postgres/audit_ledger_store_test.go AUDITAPPEND-L2-FAILURE-PROOF-01)
// but injects failure at the writer layer (where issue #655 specifies — "故意
// fail writer") rather than the txRunner closure. Drives the full HTTP →
// service-token-auth → handler → service → persistChange → emitter →
// writer.Write chain so the rollback proof covers the L2 production path
// rather than the slice service in isolation.
//
// ref: adapters/postgres/audit_ledger_store_test.go:335-392
// ref: Transactional Outbox Pattern (Microservices Patterns, Chris Richardson)
func TestL2_RbacAssign_OutboxWriteFailure_RollsBack(t *testing.T) {
	sw := newSelectiveFailWriter()
	h := newL2HarnessWithWriter(t, sw)

	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	const victimUsername = "l2-rbac-atomicity-assign-user"
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken,
		victimUsername, "rbac-atomicity-assign@l2.local", "VictimPass!99")

	require.Equal(t, 0, roleAssignmentCount(t, h, victimID, "editor"),
		"baseline: victim must not yet hold editor role")

	// Trigger window: only event.role.assigned.v1 will fail at the writer.
	sw.failType.Store(topicRoleAssignedV1)

	status := postRoleEndpoint(t, h, "assign", victimID, "editor")
	require.GreaterOrEqual(t, status, 500,
		"role assign must surface 5xx when outbox writer fails (got %d)", status)
	require.Less(t, status, 600,
		"role assign must surface 5xx (not pass through 2xx) when outbox writer fails (got %d)", status)

	// Rollback proof: domain write did NOT persist despite RoleRepo.AssignToUser
	// returning success — the TxManager rolled back when the writer failed.
	assert.Equal(t, 0, roleAssignmentCount(t, h, victimID, "editor"),
		"role_assignments row MUST NOT persist after outbox-write failure rollback")

	// Negative control: with the trigger cleared, the same call path must
	// succeed and the row must appear. Guards against the false-positive
	// where setup itself is broken and the prior assertion would trivially
	// hold regardless of rollback semantics.
	sw.failType.Store("")
	assignRole(t, h, victimID, "editor") // existing helper requires 201
	assert.Equal(t, 1, roleAssignmentCount(t, h, victimID, "editor"),
		"control: role_assignments row MUST persist on happy path")
}

// ---------------------------------------------------------------------------
// TestL2_RbacRevoke_OutboxWriteFailure_RollsBack (RBACASSIGN-L2-PG-ATOMICITY-01)
// ---------------------------------------------------------------------------

// TestL2_RbacRevoke_OutboxWriteFailure_RollsBack proves the stronger L2
// invariant for the Revoke path: when the outbox writer fails, the
// credentialinvalidate.Invalidator cascade
// (BumpAuthzEpoch + RevokeForSubject + RevokeUser) rolls back atomically
// alongside the role deletion. Verifies four DB columns are restored to
// their pre-call values, proving funnel co-rollback at L2.
//
// Setup is driven through the pass-through writer path so role assignment +
// login + session/refresh chain creation all succeed normally. The failure
// window is opened only for event.role.revoked.v1 immediately before the
// revoke call.
func TestL2_RbacRevoke_OutboxWriteFailure_RollsBack(t *testing.T) {
	sw := newSelectiveFailWriter()
	h := newL2HarnessWithWriter(t, sw)

	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	const victimUsername = "l2-rbac-atomicity-revoke-user"
	const victimPassword = "VictimPass!99"
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken,
		victimUsername, "rbac-atomicity-revoke@l2.local", victimPassword)

	// Setup phase: pass-through writer. Assign role + create 2 live sessions
	// (each session issues 1 refresh chain → 2 live refresh chains for the
	// subject).
	assignRole(t, h, victimID, "editor")
	_ = httpLogin(t, h.base, victimUsername, victimPassword)
	_ = httpLogin(t, h.base, victimUsername, victimPassword)

	require.Equal(t, 1, roleAssignmentCount(t, h, victimID, "editor"),
		"setup: editor role must be assigned before revoke attempt")
	require.Equal(t, 2, countLiveSessions(t, h, victimID),
		"setup: victim must have 2 live sessions before revoke attempt")
	require.Equal(t, 2, countLiveRefreshTokensForSubject(t, h, victimID),
		"setup: victim must have 2 live refresh chains before revoke attempt")
	epochBefore := queryUserAuthzEpoch(t, h, victimID)

	// Trigger window: only event.role.revoked.v1 will fail at the writer.
	sw.failType.Store(topicRoleRevokedV1)

	status := postRoleEndpoint(t, h, "revoke", victimID, "editor")
	require.GreaterOrEqual(t, status, 500,
		"role revoke must surface 5xx when outbox writer fails (got %d)", status)
	require.Less(t, status, 600,
		"role revoke must surface 5xx (not pass through 2xx) when outbox writer fails (got %d)", status)

	// Funnel co-rollback proof: all four mutations must be undone atomically
	// with the outbox failure. The credentialinvalidate.Invalidator funnel
	// (BumpAuthzEpoch + RevokeForSubject + RevokeUser) is wired into the same
	// txCtx as the role-repo mutation; the writer failure short-circuits the
	// closure before commit, and the TxManager rolls back every PG mutation.
	assert.Equal(t, 1, roleAssignmentCount(t, h, victimID, "editor"),
		"role_assignments row MUST NOT be removed after outbox-write failure rollback")
	assert.Equal(t, epochBefore, queryUserAuthzEpoch(t, h, victimID),
		"users.authz_epoch MUST NOT advance after outbox-write failure rollback")
	assert.Equal(t, 2, countLiveSessions(t, h, victimID),
		"sessions MUST NOT be revoked after outbox-write failure rollback")
	assert.Equal(t, 2, countLiveRefreshTokensForSubject(t, h, victimID),
		"refresh chains MUST NOT be revoked after outbox-write failure rollback")

	// Negative control: with the trigger cleared, the revoke must succeed
	// and all four state fields must flip — proving the funnel is wired
	// (without this control, the prior assertions could trivially hold even
	// if BumpAuthzEpoch / RevokeForSubject / RevokeUser were no-ops).
	sw.failType.Store("")
	revokeRole(t, h, victimID, "editor") // existing helper requires 200

	assert.Equal(t, 0, roleAssignmentCount(t, h, victimID, "editor"),
		"control: role_assignments row MUST be removed on happy revoke")
	assert.Greater(t, queryUserAuthzEpoch(t, h, victimID), epochBefore,
		"control: users.authz_epoch MUST advance on happy revoke")
	assert.Equal(t, 0, countLiveSessions(t, h, victimID),
		"control: sessions MUST be revoked on happy revoke")
	assert.Equal(t, 0, countLiveRefreshTokensForSubject(t, h, victimID),
		"control: refresh chains MUST be revoked on happy revoke")
}
