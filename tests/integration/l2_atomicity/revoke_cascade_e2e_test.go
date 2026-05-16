//go:build integration

package l2_atomicity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth"
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
	assignRole(t, h, "accesscore", victimID, "editor")

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

	// Revoke "editor" via internal listener — triggers same-tx
	// credentialinvalidate funnel.
	revokeRole(t, h, "accesscore", victimID, "editor")

	// Eventual terminal state: authz_epoch advanced + all victim sessions revoked.
	require.Eventually(t, func() bool {
		return queryUserAuthzEpoch(t, h, victimID) > epochBefore
	}, 5*time.Second, 50*time.Millisecond,
		"users.authz_epoch must advance after role revoke cascade (epochBefore=%d)", epochBefore)

	require.Eventually(t, func() bool {
		return countLiveSessions(t, h, victimID) == 0
	}, 5*time.Second, 50*time.Millisecond,
		"all victim sessions must be revoked after role revoke cascade")

	// PG confirmation: role_assignments row is gone.
	require.NoError(t, h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM role_assignments WHERE user_id = $1 AND role_id = 'editor'`,
		victimID).Scan(&roleCount))
	assert.Equal(t, 0, roleCount, "editor role assignment must be removed after revoke")
}

// assignRole calls POST /internal/v1/access/roles/assign with a service token.
func assignRole(t *testing.T, h *l2Harness, callerCell, userID, roleID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"userId": userID, "roleId": roleID})
	token := auth.GenerateServiceToken(h.ring, callerCell, http.MethodPost,
		"/internal/v1/access/roles/assign", "", time.Now())
	req, _ := http.NewRequest(http.MethodPost, h.internalBase+"/internal/v1/access/roles/assign",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "ServiceToken "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"role assign must return 201; body=%s", respBody)
}

// revokeRole calls POST /internal/v1/access/roles/revoke with a service token.
func revokeRole(t *testing.T, h *l2Harness, callerCell, userID, roleID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"userId": userID, "roleId": roleID})
	token := auth.GenerateServiceToken(h.ring, callerCell, http.MethodPost,
		"/internal/v1/access/roles/revoke", "", time.Now())
	req, _ := http.NewRequest(http.MethodPost, h.internalBase+"/internal/v1/access/roles/revoke",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "ServiceToken "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"role revoke must return 200; body=%s", respBody)
}
