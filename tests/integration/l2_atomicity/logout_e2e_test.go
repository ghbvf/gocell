//go:build integration

package l2_atomicity

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestL2_LogoutInvalidatesSession verifies the session-delete (logout) path:
//
//  1. victim user logs in — receives accessToken + sessionID
//  2. DELETE /api/v1/access/sessions/{sessionID} with Bearer accessToken → 204
//  3. subsequent request to a JWT-guarded endpoint with the same accessToken → 401
//     ERR_AUTH_UNAUTHORIZED (session revoked; AuthMiddleware enumerates-safe collapse)
//
// Source of truth:
//   - contracts/http/auth/session/delete/v1/contract.yaml (successStatus: 204)
//   - cells/accesscore/slices/sessionlogout/service.go (owner-guard + revoke)
//   - kernel/auth/middleware — epoch/session-state collapse to ERR_AUTH_UNAUTHORIZED
func TestL2_LogoutInvalidatesSession(t *testing.T) {
	h := newL2Harness(t)

	const victimUsername = "l2-logout-user"
	const victimPassword = "VictimPass!99"

	// Create a non-admin victim to avoid the last-admin guard on the admin account.
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	victimID := httpCreateUser(t, h.base, adminLogin.AccessToken, victimUsername, "logout@l2.local", victimPassword)

	// Victim logs in.
	victimLogin := httpLogin(t, h.base, victimUsername, victimPassword)
	require.NotEmpty(t, victimLogin.AccessToken)
	require.NotEmpty(t, victimLogin.SessionID)

	// Baseline: fresh token can access a JWT-guarded endpoint.
	httpGetUser(t, h.base, victimLogin.AccessToken, victimID)

	// Logout: DELETE the session.
	httpLogout(t, h.base, victimLogin.AccessToken, victimLogin.SessionID)

	// Verify the session is now revoked in PG.
	require.Equal(t, 0, countLiveSessions(t, h, victimID),
		"session must be revoked after logout")

	// The same accessToken must now be rejected.
	env := httpGetUserExpectError(t, h.base, victimLogin.AccessToken, victimID, http.StatusUnauthorized)
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", env.Error.Code,
		"post-logout accessToken must be rejected with ERR_AUTH_UNAUTHORIZED (enumeration defense)")
}
