//go:build integration

package l2atomicity

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestL2_ValidateEpochMismatch verifies the PR #490 (S4b) + S4d row-provenance
// closure: every JWT-guarded request walks sessionvalidate.enforceSessionState,
// which loads users.authz_epoch and sessions.authz_epoch_at_issue and rejects
// the token when the session's pinned epoch falls below the current user
// epoch. The authz_epoch claim was removed from the JWT in S4d; provenance
// is read from the session row.
//
// Wire shape: AuthMiddleware collapses every JWT verification failure to the
// generic ERR_AUTH_UNAUTHORIZED to prevent token-state enumeration (granular
// reasons live in slog + the auth_token_verify_total `reason` metric label).
//
// Scope note: this test does not exercise infra-error → 503 mapping. That
// behavior is statically guarded by archtest sessionvalidate_epoch_compare_test.go
// (PR #490 Medium). e2e duplication of fault injection adds no enforcement
// value here.
func TestL2_ValidateEpochMismatch(t *testing.T) {
	h := newL2Harness(t)

	const victimUsername = "l2-epoch-user"
	const victimPassword = "VictimPass!99"

	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken, victimUsername, "epoch@l2.local", victimPassword)

	victimLogin := httpLogin(t, h.base, victimUsername, victimPassword)
	require.NotEmpty(t, victimLogin.AccessToken)

	// Baseline: a fresh access token can read the user's own profile.
	httpGetUser(t, h.base, victimLogin.AccessToken, victimID)

	epochBefore := queryUserAuthzEpoch(t, h, victimID)
	bumpUserAuthzEpoch(t, h, victimID)
	epochAfter := queryUserAuthzEpoch(t, h, victimID)
	require.Greater(t, epochAfter, epochBefore,
		"direct UPDATE must advance authz_epoch (epochBefore=%d, epochAfter=%d)", epochBefore, epochAfter)

	// The previously-issued session row pinned authz_epoch_at_issue=epochBefore;
	// users.authz_epoch is now greater. sessionvalidate.enforceSessionState
	// loads both and rejects the token.
	env := httpGetUserExpectError(t, h.base, victimLogin.AccessToken, victimID, http.StatusUnauthorized)
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", env.Error.Code,
		"epoch-mismatch JWT must surface as the generic ERR_AUTH_UNAUTHORIZED (enumeration defense)")
}
