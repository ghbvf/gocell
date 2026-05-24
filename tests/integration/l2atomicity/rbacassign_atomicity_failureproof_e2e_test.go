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
// fail in the inject-failure window when the new event type fails to be
// intercepted. The negative-control path clears failType and is topic-agnostic
// by design.
const (
	eventRoleAssignedV1 = "event.role.assigned.v1"
)

// victimPassword is the test fixture password used in both atomicity tests.
// Test fixture; ephemeral integration env.
const victimPassword = "VictimPass!99"

// simulatedOutboxErr is the sentinel returned by selectiveFailWriter when it
// chooses to fail. The contents are observable only in slog (the framework
// span recorder redacts before reaching the wire); we only assert that the
// HTTP layer surfaces a 500 envelope.
var simulatedOutboxErr = errors.New("simulated outbox write failure (RBACASSIGN-L2-PG-ATOMICITY-01)")

// selectiveFailWriter wraps a real outbox.Writer and returns simulatedOutboxErr
// when an entry's EventType matches failType.Load().(string). Atomic store
// allows tests to flip behavior mid-run without rebuilding the harness:
// "setup phase passes through → trigger window fails on specific event class".
//
// The inner writer is the real adapterpg.NewOutboxWriter so the pass-through
// path exercises full PG outbox semantics; only the fail path short-circuits.
type selectiveFailWriter struct {
	inner     outbox.Writer
	failType  atomic.Value // string
	failCount atomic.Int64
}

func (w *selectiveFailWriter) Write(ctx context.Context, entry outbox.Entry) error {
	if t, _ := w.failType.Load().(string); t != "" && entry.EventType == t {
		w.failCount.Add(1)
		return simulatedOutboxErr
	}
	return w.inner.Write(ctx, entry)
}

// newSelectiveFailWriter constructs a selectiveFailWriter that delegates to the
// provided inner writer and starts in pass-through mode. The caller supplies
// inner explicitly so the injected writer and the cell-wired writer are
// provably the same instance.
func newSelectiveFailWriter(inner outbox.Writer) *selectiveFailWriter {
	w := &selectiveFailWriter{inner: inner}
	w.failType.Store("")
	return w
}

// roleAssignmentCount returns the number of role_assignments rows for
// (userID, roleID). Inlined rather than added to helpers_test.go because
// it is only used by the two atomicity tests in this file (Assign and Revoke);
// if a test outside this file adopts the same query, hoist then.
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
//
// The caller owns all status-code assertions; this helper does not require any specific status.
func postRoleEndpoint(t *testing.T, h *l2Harness, action, userID, roleID string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"userId": userID, "roleId": roleID})
	var path string
	switch action {
	case "assign":
		path = internalPathRolesAssign
	case "revoke":
		path = internalPathRolesRevoke
	default:
		t.Fatalf("postRoleEndpoint: unknown action %q (expected \"assign\" or \"revoke\")", action)
	}
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
// TestL2Atomicity_rbacassign_RollsBack (L2-OUTBOX-ATOMICITY-COVERAGE-01)
// ---------------------------------------------------------------------------

// TestL2Atomicity_rbacassign_RollsBack proves that when the outbox
// writer fails inside rbacassign.Service.Assign's transaction, the domain
// write (role_assignments INSERT via PGRoleRepo.AssignToUser) rolls back
// atomically and no row persists.
//
// Mirrors TestL2Atomicity_auditcore_RollsBack
// (adapters/postgres/audit_ledger_store_test.go)
// but injects failure at the writer layer (where issue #655 specifies — "故意
// fail writer") rather than the txRunner closure. Drives the full HTTP →
// service-token-auth → handler → service → persistChange → emitter →
// writer.Write chain so the rollback proof covers the L2 production path
// rather than the slice service in isolation.
//
// ref: adapters/postgres/audit_ledger_store_test.go:335-392
// ref: Transactional Outbox Pattern (Microservices Patterns, Chris Richardson)
func TestL2Atomicity_rbacassign_RollsBack(t *testing.T) {
	sw := newSelectiveFailWriter(adapterpg.NewOutboxWriter(clock.Real()))
	h := newL2HarnessWithWriter(t, sw)
	ctx := context.Background()

	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	const victimUsername = "l2-rbac-atomicity-assign-user"
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken,
		victimUsername, "rbac-atomicity-assign@l2.local", victimPassword)

	require.Equal(t, 0, roleAssignmentCount(t, h, victimID, "editor"),
		"baseline: victim must not yet hold editor role")

	auditBefore := countAuditEntries(t, ctx, h, eventRoleAssignedV1)

	// Trigger window: only event.role.assigned.v1 will fail at the writer.
	sw.failType.Store(eventRoleAssignedV1)

	status := postRoleEndpoint(t, h, "assign", victimID, "editor")
	require.Equal(t, http.StatusInternalServerError, status,
		"role assign must surface 500 when outbox writer fails")

	require.Equal(t, int64(1), sw.failCount.Load(),
		"writer.Write must have been invoked exactly once before transaction rollback")

	// Rollback proof: domain write did NOT persist despite RoleRepo.AssignToUser
	// returning success — the TxManager rolled back when the writer failed.
	require.Equal(t, 0, roleAssignmentCount(t, h, victimID, "editor"),
		"role_assignments row MUST NOT persist after outbox-write failure rollback")

	// Audit-silence proof: no audit entry must appear for the aborted assign.
	require.Equal(t, auditBefore, countAuditEntries(t, ctx, h, eventRoleAssignedV1),
		"rollback: no audit entry must appear for aborted assign")

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
// TestL2Atomicity_rbacassign_RollsBack_Revoke (L2-OUTBOX-ATOMICITY-COVERAGE-01)
// ---------------------------------------------------------------------------

// TestL2Atomicity_rbacassign_RollsBack_Revoke proves the stronger L2
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
func TestL2Atomicity_rbacassign_RollsBack_Revoke(t *testing.T) {
	sw := newSelectiveFailWriter(adapterpg.NewOutboxWriter(clock.Real()))
	h := newL2HarnessWithWriter(t, sw)
	ctx := context.Background()

	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	const victimUsername = "l2-rbac-atomicity-revoke-user"
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

	auditBefore := countAuditEntries(t, ctx, h, eventRoleRevokedV1)

	// Trigger window: only event.role.revoked.v1 will fail at the writer.
	sw.failType.Store(eventRoleRevokedV1)

	status := postRoleEndpoint(t, h, "revoke", victimID, "editor")
	require.Equal(t, http.StatusInternalServerError, status,
		"role revoke must surface 500 when outbox writer fails")

	require.Equal(t, int64(1), sw.failCount.Load(),
		"writer.Write must have been invoked exactly once before transaction rollback")

	// Funnel co-rollback proof: all four mutations must be undone atomically
	// with the outbox failure. The credentialinvalidate.Invalidator funnel
	// (BumpAuthzEpoch + RevokeForSubject + RevokeUser) is wired into the same
	// txCtx as the role-repo mutation; the writer failure short-circuits the
	// closure before commit, and the TxManager rolls back every PG mutation.
	require.Equal(t, 1, roleAssignmentCount(t, h, victimID, "editor"),
		"role_assignments row MUST NOT be removed after outbox-write failure rollback")
	require.Equal(t, epochBefore, queryUserAuthzEpoch(t, h, victimID),
		"users.authz_epoch MUST NOT advance after outbox-write failure rollback")
	require.Equal(t, 2, countLiveSessions(t, h, victimID),
		"sessions MUST NOT be revoked after outbox-write failure rollback")
	require.Equal(t, 2, countLiveRefreshTokensForSubject(t, h, victimID),
		"refresh chains MUST NOT be revoked after outbox-write failure rollback")

	// Audit-silence proof: no audit entry must appear for the aborted revoke.
	require.Equal(t, auditBefore, countAuditEntries(t, ctx, h, eventRoleRevokedV1),
		"rollback: no audit entry must appear for aborted revoke")

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
