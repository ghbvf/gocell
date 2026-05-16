//go:build integration

package l2_atomicity

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestL2_LoginRefreshValidate_HappyPath verifies the cross-layer L2 happy
// path that PR #482 (S4a durable session/refresh) + PR #490 (S4b authz_epoch
// closed loop) + S4d (row-provenance epoch) jointly delivered:
//
//   - login 201 commits one sessions row + one refresh_tokens row + one
//     outbox event.session.created.v1 entry atomically (single PG tx)
//   - access JWT carries jti + sid claims (epoch claim was removed in S4d;
//     epoch provenance lives on sessions.authz_epoch_at_issue)
//   - refresh 200 issues a new access/refresh pair while preserving the same
//     sid (OAuth2/OIDC sid invariant — PR #482 stable-sid pin)
//   - validate via JWT-guarded endpoint passes with the refreshed token
//
// Source of truth:
//   - cells/accesscore/slices/sessionlogin/service.go::Login (FOR UPDATE pin)
//   - cells/accesscore/slices/sessionrefresh/service.go::Refresh (sid stable)
//   - ADR 202605101400 §A1 / §D2 (S4d row-provenance epoch)
//   - adapters/postgres/migrations/026_restore_sessions_authz_epoch_at_issue.sql
func TestL2_LoginRefreshValidate_HappyPath(t *testing.T) {
	h := newL2Harness(t)
	ctx := context.Background()

	const username = "l2-happy-user"
	const password = "HappyUserPass!00"
	adminLogin := httpLogin(t, h.base, adminUsername, adminPassword)
	userID := httpCreateUser(t, h.base, adminLogin.AccessToken, username, "happy@l2.local", password)

	// Snapshot DB counts before login.
	var sessionsBefore, rtBefore, outboxBefore int
	require.NoError(t, h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE subject_id = $1`, userID).Scan(&sessionsBefore))
	require.NoError(t, h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM refresh_tokens`).Scan(&rtBefore))
	require.NoError(t, h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM outbox_entries WHERE event_type = $1`,
		"event.session.created.v1").Scan(&outboxBefore))

	// 1. Login: 201 + atomic commit of sessions/refresh_tokens/outbox.
	res := httpLogin(t, h.base, username, password)
	require.NotEmpty(t, res.AccessToken)
	require.NotEmpty(t, res.RefreshToken)
	require.NotEmpty(t, res.SessionID)

	assert.Equal(t, 1+sessionsBefore, countLiveSessions(t, h, userID),
		"login must commit exactly one sessions row")
	assert.Equal(t, 1, countLiveRefreshTokens(t, h, res.SessionID),
		"login must commit exactly one live refresh_token row for the session")

	var outboxAfter int
	require.NoError(t, h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM outbox_entries WHERE event_type = $1`,
		"event.session.created.v1").Scan(&outboxAfter))
	assert.Equal(t, outboxBefore+1, outboxAfter,
		"login must commit one event.session.created.v1 outbox entry in the same tx")

	// 2. Access JWT claims: jti + sid present, sub matches. authz_epoch was
	// removed from the JWT in S4d — epoch provenance now lives on the
	// sessions.authz_epoch_at_issue row column.
	loginClaims := decodeJWTClaims(t, res.AccessToken)
	assert.NotEmpty(t, loginClaims.JTI, "login access token must carry jti claim")
	assert.Equal(t, res.SessionID, loginClaims.SessionID, "JWT sid must equal HTTP sessionId")
	assert.Equal(t, userID, loginClaims.Subject)

	// 3. Row-side epoch provenance: sessions.authz_epoch_at_issue must match
	// users.authz_epoch at login time (S4d row-provenance invariant).
	rowEpoch := queryUserAuthzEpoch(t, h, userID)
	var sessionEpochAtIssue int64
	require.NoError(t, h.pool.DB().QueryRow(ctx,
		`SELECT authz_epoch_at_issue FROM sessions WHERE id = $1`, res.SessionID).Scan(&sessionEpochAtIssue))
	assert.Equal(t, rowEpoch, sessionEpochAtIssue,
		"sessions.authz_epoch_at_issue must equal users.authz_epoch at login time (S4d row provenance)")

	// 4. Validate fresh access token via JWT-guarded endpoint.
	_ = httpGetUserExpect(t, h.base, res.AccessToken, userID, http.StatusOK)

	// 5. Refresh: 200 + same sid (OAuth2/OIDC sid stable invariant).
	refreshed := httpRefresh(t, h.base, res.RefreshToken)
	require.NotEmpty(t, refreshed.AccessToken)
	require.NotEmpty(t, refreshed.RefreshToken)
	assert.Equal(t, res.SessionID, refreshed.SessionID,
		"refresh must preserve sid (PR #482 stable-sid invariant); login sid=%s, refresh sid=%s",
		res.SessionID, refreshed.SessionID)
	assert.NotEqual(t, res.RefreshToken, refreshed.RefreshToken,
		"refresh must rotate the refresh token (consumed parent vs fresh child)")
	assert.NotEqual(t, res.AccessToken, refreshed.AccessToken,
		"refresh must issue a fresh access token")

	// 6. Refreshed access JWT carries the same sid.
	refreshedClaims := decodeJWTClaims(t, refreshed.AccessToken)
	assert.Equal(t, res.SessionID, refreshedClaims.SessionID,
		"refreshed JWT sid must equal original sid (stable-sid)")
	assert.NotEmpty(t, refreshedClaims.JTI, "refreshed JWT must carry jti claim")

	// 7. Validate refreshed access token via JWT-guarded endpoint.
	_ = httpGetUserExpect(t, h.base, refreshed.AccessToken, userID, http.StatusOK)
}
