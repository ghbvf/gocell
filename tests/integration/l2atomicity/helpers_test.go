//go:build integration

package l2atomicity

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

	"github.com/stretchr/testify/assert"
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

// httpLoginExpect401Raw POSTs /api/v1/access/sessions/login, expects 401, and
// returns the raw response body bytes. Used by the three-way envelope
// equality check so the test can compare entire wire shapes (not just the
// code+message fields), defending against PII leakage via details / unknown
// fields added by a future regression.
func httpLoginExpect401Raw(t *testing.T, base, username, password string) []byte {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := httpClient.Post(base+"/api/v1/access/sessions/login", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "login must return 401; body=%s", raw)
	return raw
}

// normalizeErrorEnvelope parses an error-response-v1 JSON body, asserts the
// envelope's structural invariants (top-level only "error" key; error fields
// ⊆ {code, message, details, request_id}; details is an empty array on
// these uniform-401 responses), strips the runtime-injected request_id, and
// returns the normalized JSON for byte-equal comparison across multiple
// responses. Defends against unknown-field regressions (e.g. an internal
// reason leaking into the wire) and PII leakage via details.
func normalizeErrorEnvelope(t *testing.T, raw []byte) []byte {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc), "error envelope must be valid JSON")
	require.Len(t, doc, 1, "envelope must contain exactly one top-level key; got %v", keysOf(doc))
	errVal, ok := doc["error"].(map[string]any)
	require.True(t, ok, `envelope must contain an "error" object`)
	allowed := map[string]struct{}{"code": {}, "message": {}, "details": {}, "request_id": {}}
	for k := range errVal {
		_, allowedKey := allowed[k]
		require.True(t, allowedKey, "error object contains disallowed field %q", k)
	}
	if d, present := errVal["details"]; present {
		details, isArr := d.([]any)
		require.True(t, isArr, "error.details must be an array")
		assert.Empty(t, details,
			"login uniform-401 must not leak per-case details (account-enumeration defense)")
	}
	delete(errVal, "request_id")
	normalized, err := json.Marshal(doc)
	require.NoError(t, err)
	return normalized
}

// keysOf returns the keys of a map in deterministic order — used only for
// error messages.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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

// countPublishedOutboxEntries returns the number of outbox_entries rows for
// the given event_type whose published_at column has been set by
// runtime/outbox.Relay's writeBack. Used by cascade tests to prove the
// producer → relay → publisher chain link without coupling to subscriber
// success semantics (subscribers may legitimately reject and route to DLX).
// Filtering by event_type locks the assertion on a specific event class.
func countPublishedOutboxEntries(t *testing.T, ctx context.Context, h *l2Harness, eventType string) int {
	t.Helper()
	var n int
	err := h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM outbox_entries WHERE event_type = $1 AND published_at IS NOT NULL`,
		eventType).Scan(&n)
	require.NoError(t, err)
	return n
}

// publishedOutboxRow is the subset of an outbox_entries row the cascade tests
// need to validate payload contents.
type publishedOutboxRow struct {
	Payload []byte
}

// latestPublishedOutboxEntry returns the most recently published outbox_entries
// row for the given event_type. Fails if zero published rows exist.
func latestPublishedOutboxEntry(t *testing.T, ctx context.Context, h *l2Harness, eventType string) publishedOutboxRow {
	t.Helper()
	var row publishedOutboxRow
	err := h.pool.DB().QueryRow(ctx,
		`SELECT payload FROM outbox_entries
		 WHERE event_type = $1 AND published_at IS NOT NULL
		 ORDER BY published_at DESC LIMIT 1`,
		eventType).Scan(&row.Payload)
	require.NoError(t, err,
		"expected at least one published outbox row for event %s", eventType)
	return row
}

// countLiveRefreshTokensForSubject returns the count of live (not revoked) refresh
// tokens across all sessions for the subject. Used by cascade tests to confirm
// credentialinvalidate.Invalidator.RevokeUser actually swept the subject's chains
// (RevokeUser is the third op of Apply — separate from RevokeForSubject which
// hits sessions; a no-op RevokeUser would leave refresh rows live even after
// session.revoked_at is set).
func countLiveRefreshTokensForSubject(t *testing.T, h *l2Harness, subjectID string) int {
	t.Helper()
	var n int
	err := h.pool.DB().QueryRow(context.Background(),
		`SELECT count(*) FROM refresh_tokens WHERE subject_id = $1 AND revoked_at IS NULL`,
		subjectID).Scan(&n)
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
