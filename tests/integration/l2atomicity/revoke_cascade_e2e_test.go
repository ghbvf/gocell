//go:build integration

package l2atomicity

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// eventRoleRevokedV1 mirrors cells/accesscore/internal/dto.TopicRoleRevoked.
// The constant lives in an internal package that tests/integration/l2atomicity
// cannot import; per cell-patterns.md the duplication is "expected cost" of
// cell isolation. If the producer-side constant changes, this test must be
// updated in lockstep — and will visibly fail when the new event type fails
// to appear in the audit chain.
const eventRoleRevokedV1 = "event.role.revoked.v1"

// TestL2_RbacRevokeRevokesSessions verifies the cross-cell L2 cascade path:
//
//  1. seed admin assigns "editor" role to a fresh victim user via the internal
//     listener (POST /internal/v1/access/roles/assign, service token auth)
//  2. victim logs in twice, producing two live sessions
//  3. admin revokes the "editor" role via internal listener
//     (POST /internal/v1/access/roles/revoke) — same-tx credentialinvalidate
//     funnel: BumpAuthzEpoch + RevokeForSubject + RevokeUser
//  4. require.Eventually polls users.authz_epoch and the session count for the
//     victim until the cascade settles
//
// This is the e2e regression for B2-C-13: "L2 cross-layer e2e gap" — the
// existing service-layer integration test (cells/accesscore/auth_integration_test.go)
// uses a stub outbox; T4 drives the same path via real HTTP + real PG +
// in-process eventbus subscriber.
func TestL2_RbacRevokeRevokesSessions(t *testing.T) {
	h := newL2Harness(t)
	ctx := context.Background()

	const victimUsername = "l2-revoke-user"
	const victimPassword = "VictimPass!99"

	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken, victimUsername, "revoke@l2.local", victimPassword)

	// Assign "editor" role to victim (admin authority, internal listener).
	assignRole(t, h, victimID, "editor")

	// Confirm assignment landed in PG.
	var roleCount int
	require.NoError(t, h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM role_assignments WHERE user_id = $1 AND role_id = 'editor'`,
		victimID).Scan(&roleCount))
	require.Equal(t, 1, roleCount, "editor role must be assigned before revoke test")

	// Victim establishes two live sessions.
	_ = httpLogin(t, h.base, victimUsername, victimPassword)
	_ = httpLogin(t, h.base, victimUsername, victimPassword)
	require.Equal(t, 2, countLiveSessions(t, h, victimID),
		"victim must have 2 live sessions before revoke")

	epochBefore := queryUserAuthzEpoch(t, h, victimID)
	// Snapshot how many event.role.revoked.v1 rows the outbox has published
	// before the revoke. published_at is set by runtime/outbox.Relay's
	// writeBack only after the publisher returns success, so an unchanged
	// count would prove the relay never drained this revoke's outbox row.
	// Filtering by event_type locks the assertion on this specific event
	// class — any concurrent event.role.assigned.v1 / event.session.* row
	// being relay-published would otherwise satisfy a generic "more rows
	// published" check.
	revokedPublishedBefore := countPublishedOutboxEntries(t, ctx, h, eventRoleRevokedV1)

	// Revoke "editor" via internal listener — triggers same-tx
	// credentialinvalidate funnel.
	revokeRole(t, h, victimID, "editor")

	// Eventual terminal state: authz_epoch advanced + all victim sessions revoked.
	//
	// rbacassign.Revoke runs the credentialinvalidate.Apply funnel inside the
	// same tx, so the row mutations commit before the 200 response. The
	// require.Eventually wrapper absorbs the small CI-side gap between handler
	// completion and SELECT visibility under load; the cascade itself is not
	// eventually-consistent.
	require.Eventually(t, func() bool {
		return queryUserAuthzEpoch(t, h, victimID) > epochBefore
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"users.authz_epoch must advance after role revoke cascade (epochBefore=%d)", epochBefore)

	require.Eventually(t, func() bool {
		return countLiveSessions(t, h, victimID) == 0
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"all victim sessions must be revoked after role revoke cascade")

	// PG confirmation: role_assignments row is gone.
	require.NoError(t, h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM role_assignments WHERE user_id = $1 AND role_id = 'editor'`,
		victimID).Scan(&roleCount))
	assert.Equal(t, 0, roleCount, "editor role assignment must be removed after revoke")

	// Real producer → relay → publisher evidence: rbacassign's L2
	// event.role.revoked.v1 outbox row must be drained by runtime/outbox.Relay
	// and its published_at column set by writeBack. The previous
	// Tail().SeqNo-only check was too loose — any concurrent event of any
	// type would have advanced SeqNo. Filtering published-outbox rows by
	// event_type locks the assertion on this specific event class.
	//
	// Note on the consumer link: in this harness auditcore subscribes to
	// role events but rejects payloads missing `actor` identity (the
	// rbacassign service-token caller cell does not populate actor in the
	// event payload — a real production gap captured separately as
	// AUDIT-ROLE-EVENT-ACTOR-PROPAGATION-01). The relay still publishes the
	// event and the accesscore rbac-session-sync subscriber receives it; the
	// auditcore rejection routes to DLX by design (DispositionReject is a
	// designed-for outcome, not a chain failure). Asserting on
	// outbox_entries.published_at therefore tests the chain link that this
	// PR actually wires (producer→relay→publisher), without coupling the
	// test to a subscriber-side gap that belongs to a separate fix.
	require.Eventually(t, func() bool {
		return countPublishedOutboxEntries(t, ctx, h, eventRoleRevokedV1) > revokedPublishedBefore
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"runtime/outbox.Relay must mark an additional event.role.revoked.v1 row as published_at after rbacassign emits (before=%d)",
		revokedPublishedBefore)

	// Payload validation on the most-recent published row — defends against
	// a future "wrong event delivered" regression: a stray
	// event.role.revoked.v1 for a different user/role would pass the count
	// check above but should not match this victim's identity.
	row := latestPublishedOutboxEntry(t, ctx, h, eventRoleRevokedV1)
	var payload struct {
		UserID string `json:"userId"`
		RoleID string `json:"roleId"`
		Action string `json:"action"`
	}
	require.NoError(t, json.Unmarshal(row.Payload, &payload),
		"outbox row payload must be a valid RoleChangedEvent JSON")
	assert.Equal(t, victimID, payload.UserID,
		"outbox row must record the revoked victim (not a stray subject)")
	assert.Equal(t, "editor", payload.RoleID,
		"outbox row must record the revoked role")
	assert.Equal(t, "revoked", payload.Action,
		"outbox row payload.action must be 'revoked'")
}
