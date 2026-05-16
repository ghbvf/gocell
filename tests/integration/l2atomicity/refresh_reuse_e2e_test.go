//go:build integration

package l2atomicity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// TestL2_RefreshReuseTriggersCascade verifies the PR #490 reuse-cascade funnel:
// after exhausting the refresh grace window, replaying the consumed parent
// triggers credentialinvalidate.Invalidator.Apply — atomic BumpAuthzEpoch +
// RevokeForSubject + RevokeUser.
//
// Sequence:
//  1. login → R1
//  2. refresh R1 → R2 (R1 rotated_at = now, used_times = 0)
//  3. replay R1 DefaultGraceMaxReuses (3) times — each is within the
//     ReuseInterval grace window so each succeeds and increments R1.used_times
//  4. replay R1 the 4th time → handleRotatedRow detects grace_exhausted →
//     401 ERR_AUTH_REFRESH_FAILED and the cascade revoke runs
//
// Post-conditions (require.Eventually because the cascade runs inside the
// refresh tx but the subscriber-driven cleanup of peer sessions may settle
// asynchronously):
//   - users.authz_epoch > original
//   - count(sessions WHERE subject = victim AND revoked_at IS NULL) == 0
func TestL2_RefreshReuseTriggersCascade(t *testing.T) {
	h := newL2Harness(t)

	const victimUsername = "l2-reuse-user"
	const victimPassword = "VictimPass!99"
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken, victimUsername, "reuse@l2.local", victimPassword)

	first := httpLogin(t, h.base, victimUsername, victimPassword)
	require.NotEmpty(t, first.RefreshToken)

	// First refresh — rotates R1, issues R2.
	second := httpRefresh(t, h.base, first.RefreshToken)
	require.NotEmpty(t, second.RefreshToken)
	assert.Equal(t, first.SessionID, second.SessionID, "refresh must preserve sid")

	// Snapshot epoch before reuse cascade.
	epochBefore := queryUserAuthzEpoch(t, h, victimID)

	// Grace retry: replay R1 GraceMaxReuses times, each succeeds.
	for i := 0; i < refresh.DefaultGraceMaxReuses; i++ {
		t.Logf("grace retry %d", i)
		got := httpRefresh(t, h.base, first.RefreshToken)
		require.NotEmpty(t, got.AccessToken, "grace retry %d must succeed", i)
	}

	// One more replay → grace exhausted → 401 + cascade.
	env := httpRefreshExpect401(t, h.base, first.RefreshToken)
	assert.Equal(t, "ERR_AUTH_REFRESH_FAILED", env.Error.Code,
		"grace-exhausted reuse must surface as ERR_AUTH_REFRESH_FAILED")

	// PG terminal state: epoch bumped + all victim sessions revoked.
	//
	// credentialinvalidate.Apply runs synchronously inside the refresh tx, so
	// the row mutations (users.authz_epoch / sessions.revoked_at /
	// refresh_tokens.revoked_at) are visible by the time the 401 HTTP response
	// returns. The require.Eventually wrapper exists only to absorb the small
	// CI-side gap between handler completion and SELECT visibility under load;
	// the cascade itself is not eventually-consistent.
	require.Eventually(t, func() bool {
		return queryUserAuthzEpoch(t, h, victimID) > epochBefore
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"users.authz_epoch must advance after reuse cascade (epochBefore=%d)", epochBefore)

	require.Eventually(t, func() bool {
		return countLiveSessions(t, h, victimID) == 0
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"all victim sessions must be revoked after reuse cascade")

	// credentialinvalidate.Apply's third op is refresh.Store.RevokeUser — without
	// this assertion the test would still pass if RevokeUser degraded to a no-op
	// (epoch bump + session revoke alone would still make access tokens 401).
	// Verify both the DB-row terminal state and the HTTP-surface effect.
	require.Eventually(t, func() bool {
		return countLiveRefreshTokensForSubject(t, h, victimID) == 0
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"all victim refresh tokens must be revoked after reuse cascade (RevokeUser third op)")

	// Even the most recently rotated child refresh token must now be rejected.
	envChild := httpRefreshExpect401(t, h.base, second.RefreshToken)
	assert.Equal(t, "ERR_AUTH_REFRESH_FAILED", envChild.Error.Code,
		"post-cascade refresh with a previously-live child token must surface ERR_AUTH_REFRESH_FAILED")
}
