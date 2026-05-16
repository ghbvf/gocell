//go:build integration

package l2atomicity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

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
	auditTailBefore, err := h.auditStore.Tail(ctx)
	require.NoError(t, err)

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

	// Real producer → relay → publisher → consumer evidence: rbacassign's
	// L2 event.role.revoked.v1 row must be drained by the outbox relay,
	// republished onto the in-process eventbus, and appended by the auditcore
	// subscriber. A no-op relay or a broken subscription would leave the
	// audit chain stalled at auditTailBefore.SeqNo even though the same-tx
	// cascade (asserted above) succeeded.
	require.Eventually(t, func() bool {
		tail, terr := h.auditStore.Tail(ctx)
		return terr == nil && tail.SeqNo > auditTailBefore.SeqNo
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"auditcore ledger Tail.SeqNo must advance after relay publishes role.revoked event (before=%d)",
		auditTailBefore.SeqNo)
}
