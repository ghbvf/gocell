//go:build integration

package l2_atomicity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth"
)

// loginResult holds the parsed login/refresh response fields.
type loginResult struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	ExpiresAt    string
}

// httpLogin POSTs /api/v1/access/sessions/login and expects 201.
func httpLogin(t *testing.T, base, username, password string) loginResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := httpClient.Post(base+"/api/v1/access/sessions/login", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "login must return 201; body=%s", respBody)

	var parsed struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			SessionID    string `json:"sessionId"`
			ExpiresAt    string `json:"expiresAt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(respBody, &parsed))
	require.NotEmpty(t, parsed.Data.AccessToken, "accessToken must be present")
	require.NotEmpty(t, parsed.Data.RefreshToken, "refreshToken must be present")
	require.NotEmpty(t, parsed.Data.SessionID, "sessionId must be present")
	return loginResult{
		AccessToken:  parsed.Data.AccessToken,
		RefreshToken: parsed.Data.RefreshToken,
		SessionID:    parsed.Data.SessionID,
		ExpiresAt:    parsed.Data.ExpiresAt,
	}
}

// httpLoginExpect401 POSTs /api/v1/access/sessions/login and expects 401, returning
// the parsed error envelope. Used by the uniform-401 wire-shape test.
func httpLoginExpect401(t *testing.T, base, username, password string) errorEnvelope {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := httpClient.Post(base+"/api/v1/access/sessions/login", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "login must return 401; body=%s", respBody)

	var env errorEnvelope
	require.NoError(t, json.Unmarshal(respBody, &env))
	return env
}

// httpRefresh POSTs /api/v1/access/sessions/refresh and expects 200.
func httpRefresh(t *testing.T, base, refreshToken string) loginResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"refreshToken": refreshToken})
	resp, err := httpClient.Post(base+"/api/v1/access/sessions/refresh", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "refresh must return 200; body=%s", respBody)

	var parsed struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			SessionID    string `json:"sessionId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(respBody, &parsed))
	return loginResult{
		AccessToken:  parsed.Data.AccessToken,
		RefreshToken: parsed.Data.RefreshToken,
		SessionID:    parsed.Data.SessionID,
	}
}

// httpRefreshExpect401 POSTs /api/v1/access/sessions/refresh expecting 401.
func httpRefreshExpect401(t *testing.T, base, refreshToken string) errorEnvelope {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"refreshToken": refreshToken})
	resp, err := httpClient.Post(base+"/api/v1/access/sessions/refresh", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "refresh must return 401; body=%s", respBody)

	var env errorEnvelope
	require.NoError(t, json.Unmarshal(respBody, &env))
	return env
}

// httpCreateUser calls POST /api/v1/access/users with an admin bearer token to
// create a non-admin user. Returns the created user ID.
func httpCreateUser(t *testing.T, base, adminAccessToken, username, email, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"email":    email,
		"password": password,
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/users", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminAccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "user creation must return 201; body=%s", respBody)

	var parsed struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(respBody, &parsed))
	require.NotEmpty(t, parsed.Data.ID, "created user must have an ID")
	return parsed.Data.ID
}

// httpLockUser calls POST /api/v1/access/users/{userID}/lock and expects 200.
// The Lock endpoint requires a JSON object body (empty or otherwise); the
// generated handler rejects nil bodies with 400 via DecodeJSONStrict.
func httpLockUser(t *testing.T, base, adminAccessToken, userID string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/users/"+userID+"/lock", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+adminAccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "user lock must return 200; body=%s", respBody)
}

// changePasswordResult is the success-path response shape from change-password.
type changePasswordResult struct {
	AccessToken           string
	RefreshToken          string
	SessionID             string
	UserID                string
	PasswordResetRequired bool
}

// httpChangePasswordOK calls POST /api/v1/access/users/{userID}/password and expects 200.
// Returns the new access/refresh token pair (success-path emits a fresh session).
func httpChangePasswordOK(t *testing.T, base, accessToken, userID, oldPass, newPass string) changePasswordResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"oldPassword": oldPass,
		"newPassword": newPass,
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/users/"+userID+"/password", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "change-password must return 200; body=%s", respBody)

	var parsed struct {
		Data struct {
			AccessToken           string `json:"accessToken"`
			RefreshToken          string `json:"refreshToken"`
			SessionID             string `json:"sessionId"`
			UserID                string `json:"userId"`
			PasswordResetRequired bool   `json:"passwordResetRequired"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(respBody, &parsed))
	return changePasswordResult{
		AccessToken:           parsed.Data.AccessToken,
		RefreshToken:          parsed.Data.RefreshToken,
		SessionID:             parsed.Data.SessionID,
		UserID:                parsed.Data.UserID,
		PasswordResetRequired: parsed.Data.PasswordResetRequired,
	}
}

// httpChangePasswordExpect calls POST /api/v1/access/users/{userID}/password and
// asserts the given non-2xx status. Returns the parsed error envelope.
func httpChangePasswordExpect(t *testing.T, base, accessToken, userID, oldPass, newPass string, expectStatus int) errorEnvelope {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"oldPassword": oldPass,
		"newPassword": newPass,
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/users/"+userID+"/password", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, expectStatus, resp.StatusCode, "change-password expected %d; body=%s", expectStatus, respBody)

	var env errorEnvelope
	require.NoError(t, json.Unmarshal(respBody, &env))
	return env
}

// httpGetUser calls GET /api/v1/access/users/{userID} and asserts 200.
func httpGetUser(t *testing.T, base, accessToken, userID string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/access/users/"+userID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET user expected 200; body=%s", respBody)
}

// httpGetUserExpectError calls GET /api/v1/access/users/{userID}, asserts the given
// non-2xx status, and returns the parsed error envelope.
func httpGetUserExpectError(t *testing.T, base, accessToken, userID string, expectStatus int) errorEnvelope {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/access/users/"+userID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, expectStatus, resp.StatusCode, "GET user expected %d; body=%s", expectStatus, respBody)

	var env errorEnvelope
	require.NoError(t, json.Unmarshal(respBody, &env))
	return env
}

// httpLogout calls DELETE /api/v1/access/sessions/{sessionID} with the given
// bearer token and expects 204.
func httpLogout(t *testing.T, base, accessToken, sessionID string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, base+"/api/v1/access/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "logout must return 204; body=%s", respBody)
}

// errorEnvelope is the standard error response shape from
// contracts/shared/errors/error-response-v1.schema.json.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// jwtClaims is the minimal subset of JWT claims the L2 tests assert on.
type jwtClaims struct {
	Subject     string `json:"sub"`
	JTI         string `json:"jti"`
	SessionID   string `json:"sid"`
	AuthzEpoch  int64  `json:"authz_epoch"`
	TokenIntent string `json:"intent"`
}

// decodeJWTClaims decodes the middle segment of a compact JWS without verifying
// the signature; for tests asserting on claim shape, not authenticity.
func decodeJWTClaims(t *testing.T, token string) jwtClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	require.GreaterOrEqual(t, len(parts), 2, "JWT must have at least header.payload")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err, "JWT payload must decode as base64url")
	var c jwtClaims
	require.NoError(t, json.Unmarshal(payload, &c))
	return c
}

// queryUserAuthzEpoch returns users.authz_epoch for the given user ID.
func queryUserAuthzEpoch(t *testing.T, h *l2Harness, userID string) int64 {
	t.Helper()
	var epoch int64
	err := h.pool.DB().QueryRow(context.Background(),
		`SELECT authz_epoch FROM users WHERE id = $1`, userID).Scan(&epoch)
	require.NoError(t, err)
	return epoch
}

// queryUserStatus returns users.status for the given user ID.
func queryUserStatus(t *testing.T, h *l2Harness, userID string) string {
	t.Helper()
	var status string
	err := h.pool.DB().QueryRow(context.Background(),
		`SELECT status FROM users WHERE id = $1`, userID).Scan(&status)
	require.NoError(t, err)
	return status
}

// bumpUserAuthzEpoch increments users.authz_epoch directly.
func bumpUserAuthzEpoch(t *testing.T, h *l2Harness, userID string) {
	t.Helper()
	_, err := h.pool.DB().Exec(context.Background(),
		`UPDATE users SET authz_epoch = authz_epoch + 1 WHERE id = $1`, userID)
	require.NoError(t, err)
}

// countLiveSessions returns the count of unrevoked sessions for a subject.
func countLiveSessions(t *testing.T, h *l2Harness, subjectID string) int {
	t.Helper()
	var n int
	err := h.pool.DB().QueryRow(context.Background(),
		`SELECT count(*) FROM sessions WHERE subject_id = $1 AND revoked_at IS NULL`,
		subjectID).Scan(&n)
	require.NoError(t, err)
	return n
}

// countLiveRefreshTokens returns the count of live (not rotated, not revoked) refresh tokens for a session.
func countLiveRefreshTokens(t *testing.T, h *l2Harness, sessionID string) int {
	t.Helper()
	var n int
	err := h.pool.DB().QueryRow(context.Background(),
		`SELECT count(*) FROM refresh_tokens WHERE session_id = $1 AND rotated_at IS NULL AND revoked_at IS NULL`,
		sessionID).Scan(&n)
	require.NoError(t, err)
	return n
}

// assignRole calls POST /internal/v1/access/roles/assign with a service token
// signed for the "accesscore" caller cell. Inlined literal is required by the
// SVCTOKEN-CALLER-CELL-REQUIRED-01 archtest: every GenerateServiceToken call
// must pass a string literal in the callerCell position so the caller-cell
// allowlist can be statically verified.
func assignRole(t *testing.T, h *l2Harness, userID, roleID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"userId": userID, "roleId": roleID})
	token := auth.GenerateServiceToken(h.ring, "accesscore", http.MethodPost,
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

// revokeRole calls POST /internal/v1/access/roles/revoke with a service token
// signed for the "accesscore" caller cell. See assignRole for why the caller
// cell is an inlined literal rather than a parameter.
func revokeRole(t *testing.T, h *l2Harness, userID, roleID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"userId": userID, "roleId": roleID})
	token := auth.GenerateServiceToken(h.ring, "accesscore", http.MethodPost,
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

