//go:build integration

package l2_atomicity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	ctx := context.Background()

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
		_ = httpRefresh(t, h.base, first.RefreshToken)
	}

	// One more replay → grace exhausted → 401 + cascade.
	env := httpRefreshExpect401(t, h.base, first.RefreshToken)
	assert.Equal(t, "ERR_AUTH_REFRESH_FAILED", env.Error.Code,
		"grace-exhausted reuse must surface as ERR_AUTH_REFRESH_FAILED")

	// PG terminal state: epoch bumped + all victim sessions revoked.
	require.Eventually(t, func() bool {
		var epoch int64
		if qErr := h.pool.DB().QueryRow(ctx,
			`SELECT authz_epoch FROM users WHERE id = $1`, victimID).Scan(&epoch); qErr != nil {
			return false
		}
		return epoch > epochBefore
	}, 5*time.Second, 50*time.Millisecond,
		"users.authz_epoch must advance after reuse cascade (epochBefore=%d)", epochBefore)

	require.Eventually(t, func() bool {
		return countLiveSessions(t, h, victimID) == 0
	}, 5*time.Second, 50*time.Millisecond,
		"all victim sessions must be revoked after reuse cascade")
}
