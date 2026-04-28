//go:build integration

// Package main — walkthrough integration test for ssobff.
//
// Verifies the complete API flow described in README.md:
//  1. Bootstrap admin credentials are written to a temp credential file
//  2. Admin logs in → response has passwordResetRequired=true
//  3. Business endpoints return 403 ERR_AUTH_PASSWORD_RESET_REQUIRED while reset pending
//  4. Change-password endpoint returns 200 + new TokenPair (passwordResetRequired=false)
//  5. New token passes through business endpoints (200)
//  6. refreshToken field works for token rotation
//  7. Logout returns 204 with empty body
//  8. Audit entries contain timestamp field (not createdAt)
//  9. Config CRUD (POST/PUT/GET) with admin token — Steps 8-11 in README
//  10. Feature flags list is accessible with auth
//
// Security regression gate (PR#172 F1): slog capture verifies no log record
// contains the bootstrap password in plaintext.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingHandler is an slog.Handler that records every log record for
// post-test inspection. It is goroutine-safe via a mutex.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
	inner   slog.Handler
}

func newCapturingHandler(inner slog.Handler) *capturingHandler {
	return &capturingHandler{inner: inner}
}

func (h *capturingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *capturingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return h.inner.Handle(ctx, r)
}

func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &capturingHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *capturingHandler) WithGroup(name string) slog.Handler {
	return &capturingHandler{inner: h.inner.WithGroup(name)}
}

// assertNoPlaintextPassword verifies that none of the captured slog records
// contain the given password in any attribute value. This is the regression
// gate for PR#172 F1 (plaintext password in structured logs).
func assertNoPlaintextPassword(t *testing.T, h *capturingHandler, password string) {
	t.Helper()
	if password == "" {
		return // no password to check
	}
	h.mu.Lock()
	records := make([]slog.Record, len(h.records))
	copy(records, h.records)
	h.mu.Unlock()

	for _, rec := range records {
		// Check the log message itself.
		if strings.Contains(rec.Message, password) {
			t.Errorf("slog record message contains plaintext password: %q", rec.Message)
		}
		// Check every attribute value.
		rec.Attrs(func(a slog.Attr) bool {
			if strings.Contains(a.Value.String(), password) {
				t.Errorf("slog record attr %q=%q contains plaintext password", a.Key, a.Value.String())
			}
			return true
		})
	}
}

// credentialFromFile reads the credential file written by the bootstrap and
// returns (username, password). The file format is:
//
//	# GoCell initial admin credential
//	username=admin
//	password=<token>
//	expires_at=<unix>
func credentialFromFile(path string) (username, password string, err error) {
	f, err := os.Open(path) //nolint:gosec // test helper reads a fixed test-temp path
	if err != nil {
		return "", "", fmt.Errorf("open credential file %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "username=") {
			username = strings.TrimPrefix(line, "username=")
		} else if strings.HasPrefix(line, "password=") {
			password = strings.TrimPrefix(line, "password=")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("scan credential file: %w", err)
	}
	if username == "" || password == "" {
		return "", "", fmt.Errorf("credential file missing username or password: %s", path)
	}
	return username, password, nil
}

// buildWalkthroughServer constructs an in-memory ssobff server through the
// same NewSSOBFFApp/bootstrap path used by main.go.
//
// It uses WithInitialAdminBootstrap so bootstrap credentials are written to
// <stateDir>/initial_admin_password. Callers read credentials from the file.
//
// The capturing slog handler is threaded through so the test can assert that
// no plaintext password appears in any log record (PR#172 F1 regression gate).
func buildWalkthroughServer(t *testing.T, stateDir string, capHandler *capturingHandler) (string, func()) {
	t.Helper()

	// Set GOCELL_STATE_DIR so the bootstrapper resolves the credential path to
	// <stateDir>/initial_admin_password.
	t.Setenv("GOCELL_STATE_DIR", stateDir)

	primary := newWalkthroughListener(t)
	internal := newWalkthroughListener(t)
	health := newWalkthroughListener(t)
	app, err := NewSSOBFFApp(
		WithSSOBFFLogger(slog.New(capHandler)),
		WithSSOBFFInternalServiceSecret("walkthrough-service-token-secret-32b"),
		WithSSOBFFListener(cell.PrimaryListener, primary),
		WithSSOBFFListener(cell.InternalListener, internal),
		WithSSOBFFListener(cell.HealthListener, health),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()
	waitForWalkthroughReady(t, app.HealthAddr(), done)

	cleanup := func() {
		cancel()
		require.NoError(t, <-done)
	}
	return "http://" + app.PrimaryAddr(), cleanup
}

func newWalkthroughListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func waitForWalkthroughReady(t *testing.T, healthAddr string, done <-chan error) {
	t.Helper()
	require.Eventually(t, func() bool {
		select {
		case err := <-done:
			require.NoError(t, err)
			t.Fatal("ssobff exited before becoming ready")
			return false
		default:
		}
		resp, err := http.Get("http://" + healthAddr + "/readyz") //nolint:noctx
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "ssobff bootstrap never became ready")
}

// TestWalkthrough exercises the complete ssobff API walkthrough.
func TestWalkthrough(t *testing.T) {
	// Use a temp dir as GOCELL_STATE_DIR so the credential file is isolated
	// to this test run. The slog capturing handler wraps a discard handler
	// so we can assert no plaintext password appears in logs.
	stateDir := t.TempDir()
	capHandler := newCapturingHandler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	base, cleanup := buildWalkthroughServer(t, stateDir, capHandler)
	defer cleanup()

	// Read bootstrap credentials from the credential file written during Init.
	credPath := filepath.Join(stateDir, "initial_admin_password")
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(credPath)
		return statErr == nil
	}, 5*time.Second, 50*time.Millisecond, "credential file must exist after Init")

	bootstrapUsername, bootstrapPassword, err := credentialFromFile(credPath)
	require.NoError(t, err, "must read credentials from credential file")
	require.NotEmpty(t, bootstrapUsername, "bootstrap username must be non-empty")
	require.NotEmpty(t, bootstrapPassword, "bootstrap password must be non-empty")

	var adminToken string
	var adminUserID string

	// Step 1: Bootstrap admin login — must return 201 + passwordResetRequired=true.
	t.Run("bootstrap admin can login and passwordResetRequired=true", func(t *testing.T) {
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, bootstrapUsername, bootstrapPassword)
		resp, err := http.Post(base+"/api/v1/access/sessions/login", //nolint:noctx
			"application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode,
			"bootstrap admin login must return 201 Created")

		var envelope struct {
			Data struct {
				AccessToken           string `json:"accessToken"`
				RefreshToken          string `json:"refreshToken"`
				ExpiresAt             string `json:"expiresAt"`
				SessionID             string `json:"sessionId"`
				UserID                string `json:"userId"`
				PasswordResetRequired bool   `json:"passwordResetRequired"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.NotEmpty(t, envelope.Data.AccessToken, "response must include accessToken")
		assert.NotEmpty(t, envelope.Data.RefreshToken, "response must include refreshToken")
		assert.NotEmpty(t, envelope.Data.ExpiresAt, "response must include expiresAt")
		assert.NotEmpty(t, envelope.Data.UserID, "response must include userId")
		assert.True(t, envelope.Data.PasswordResetRequired,
			"bootstrap admin login must return passwordResetRequired=true")

		adminToken = envelope.Data.AccessToken
		adminUserID = envelope.Data.UserID
	})

	// Guard: remaining subtests need a valid admin token.
	require.NotEmpty(t, adminToken, "adminToken must be set by admin login subtest")
	require.NotEmpty(t, adminUserID, "adminUserID must be set from login response userId field")

	// Step 2: Business endpoint with reset-required token → 403.
	t.Run("business endpoint blocked with ERR_AUTH_PASSWORD_RESET_REQUIRED", func(t *testing.T) {
		// GET /api/v1/config/site.title is an authenticated non-exempt endpoint.
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet,
			base+"/api/v1/config/site.title",
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+adminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		bodyBytes, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, readErr)

		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"business endpoint must return 403 when passwordResetRequired=true")
		assert.Contains(t, string(bodyBytes), "ERR_AUTH_PASSWORD_RESET_REQUIRED",
			"error body must contain ERR_AUTH_PASSWORD_RESET_REQUIRED")
	})

	newPassword := "NewP@ssw0rd!9876"

	// Step 3: Change password → 200 + new TokenPair + passwordResetRequired=false.
	var newAdminToken string
	t.Run("change password returns new token with passwordResetRequired=false", func(t *testing.T) {
		body := fmt.Sprintf(`{"oldPassword":%q,"newPassword":%q}`, bootstrapPassword, newPassword)
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/access/users/"+adminUserID+"/password",
			strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		bodyBytes, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, readErr)

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"POST /{id}/password must return 200, body=%s", string(bodyBytes))

		var envelope struct {
			Data struct {
				AccessToken           string `json:"accessToken"`
				RefreshToken          string `json:"refreshToken"`
				ExpiresAt             string `json:"expiresAt"`
				SessionID             string `json:"sessionId"`
				UserID                string `json:"userId"`
				PasswordResetRequired bool   `json:"passwordResetRequired"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(bodyBytes, &envelope))
		assert.NotEmpty(t, envelope.Data.AccessToken, "response must include new accessToken")
		assert.NotEmpty(t, envelope.Data.RefreshToken, "response must include new refreshToken")
		assert.NotEmpty(t, envelope.Data.SessionID, "response must include sessionId")
		assert.NotEmpty(t, envelope.Data.UserID, "response must include userId")
		assert.False(t, envelope.Data.PasswordResetRequired,
			"new token must have passwordResetRequired=false after change-password")

		newAdminToken = envelope.Data.AccessToken
	})

	require.NotEmpty(t, newAdminToken, "newAdminToken must be set by change-password subtest")

	// Step 4: Business endpoint with new token → 200 (must create config first).
	t.Run("business endpoint allowed after password change", func(t *testing.T) {
		// Create a config entry first.
		createReq, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/config/",
			strings.NewReader(`{"key":"site.title","value":"GoCell Demo","sensitive":false}`))
		require.NoError(t, err)
		createReq.Header.Set("Content-Type", "application/json")
		createReq.Header.Set("Authorization", "Bearer "+newAdminToken)

		createResp, err := http.DefaultClient.Do(createReq)
		require.NoError(t, err)
		createResp.Body.Close()
		assert.Equal(t, http.StatusCreated, createResp.StatusCode,
			"POST /config/ with new token must return 201 Created")

		// Now read it back.
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet,
			base+"/api/v1/config/site.title",
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+newAdminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"GET /config/site.title with new token must return 200 OK")
	})

	// Security regression gate (PR#172 F1): verify no slog record contains
	// the bootstrap password in plaintext.
	t.Run("slog records contain no plaintext bootstrap password", func(t *testing.T) {
		assertNoPlaintextPassword(t, capHandler, bootstrapPassword)
	})

	// --- Remaining walkthrough steps use the new token ---
	var accessToken, refreshToken, sessionID string

	t.Run("admin can create a new user", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/access/users",
			strings.NewReader(`{"username":"alice","password":"P@ssw0rd123","email":"alice@example.com"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+newAdminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode,
			"POST /users with admin token must return 201 Created")

		var envelope struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.NotEmpty(t, envelope.Data.ID, "created user must have an id")
	})

	t.Run("alice can login and returns accessToken+refreshToken+sessionId", func(t *testing.T) {
		body := `{"username":"alice","password":"P@ssw0rd123"}`
		resp, err := http.Post(base+"/api/v1/access/sessions/login", //nolint:noctx
			"application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode,
			"alice login must return 201 Created")

		var envelope struct {
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				SessionID    string `json:"sessionId"`
				UserID       string `json:"userId"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.NotEmpty(t, envelope.Data.AccessToken, "response must include accessToken")
		assert.NotEmpty(t, envelope.Data.RefreshToken, "response must include refreshToken")
		assert.NotEmpty(t, envelope.Data.SessionID, "response must include sessionId")

		accessToken = envelope.Data.AccessToken
		refreshToken = envelope.Data.RefreshToken
		sessionID = envelope.Data.SessionID
	})

	// Guard: remaining subtests need valid alice tokens.
	require.NotEmpty(t, accessToken, "accessToken must be set by alice login subtest")
	require.NotEmpty(t, refreshToken, "refreshToken must be set by alice login subtest")
	require.NotEmpty(t, sessionID, "sessionID must be set by alice login subtest")

	t.Run("refresh using refreshToken field works without Authorization header", func(t *testing.T) {
		payload := fmt.Sprintf(`{"refreshToken":%q}`, refreshToken)
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/access/sessions/refresh",
			strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		// No Authorization header — refresh is a public endpoint.

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"refresh with correct refreshToken must return 200 OK")

		var envelope struct {
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.NotEmpty(t, envelope.Data.AccessToken, "refresh response must include new accessToken")
		assert.NotEmpty(t, envelope.Data.RefreshToken, "refresh response must include new refreshToken")

		// Use the rotated token for subsequent steps.
		accessToken = envelope.Data.AccessToken
		refreshToken = envelope.Data.RefreshToken
	})

	t.Run("logout returns 204 with empty body", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodDelete,
			base+"/api/v1/access/sessions/"+sessionID,
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode,
			"logout must return 204 No Content (not 200)")

		bodyBytes, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		bodyBytes = bytes.TrimSpace(bodyBytes)
		assert.Empty(t, bodyBytes,
			"logout response body must be empty (piping to jq would fail otherwise)")
	})

	t.Run("refresh token is rejected after logout (cascade revoke end-to-end)", func(t *testing.T) {
		// F15: prove that the logout-revokes-refresh cascade works through the
		// full HTTP stack. After logout the held refreshToken must be rejected
		// with 401 (or 410 Gone if the store distinguishes revoked from expired).
		payload := fmt.Sprintf(`{"refreshToken":%q}`, refreshToken)
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/access/sessions/refresh",
			strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusGone,
			"post-logout refresh must return 401 or 410, got %d", resp.StatusCode)
	})

	t.Run("audit entries require auth and contain timestamp field not createdAt", func(t *testing.T) {
		// In demo mode, audit events are delivered async via the in-memory
		// eventbus; poll until at least one entry is visible.
		var entries []json.RawMessage
		require.Eventually(t, func() bool {
			data, ok := fetchAuditEntries(base+"/api/v1/audit/entries", newAdminToken)
			if ok {
				entries = data
			}
			return ok
		}, 2*time.Second, 50*time.Millisecond, "expected at least one audit entry")

		for _, raw := range entries {
			var entry map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &entry))

			_, hasTimestamp := entry["timestamp"]
			_, hasCreatedAt := entry["createdAt"]

			assert.True(t, hasTimestamp,
				"audit entry must have 'timestamp' field matching AuditEntryResponse DTO")
			assert.False(t, hasCreatedAt,
				"audit entry must NOT have 'createdAt' field (DTO uses 'timestamp')")
		}
	})

	// Steps 8-11: configcore CRUD + feature flags.
	// PR-CFG-C: ALL config + flags HTTP routes (read and write) require RoleAdmin —
	// even GET endpoints, because key names + the sensitive flag are themselves a
	// recon surface. Authenticated non-admin callers (e.g. alice) receive 403.

	t.Run("admin can update a config entry (PUT /api/v1/config/site.title)", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPut,
			base+"/api/v1/config/site.title",
			strings.NewReader(`{"value":"GoCell Updated"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+newAdminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"PUT /config/site.title with admin token must return 200 OK")

		var envelope struct {
			Data struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.Equal(t, "GoCell Updated", envelope.Data.Value,
			"update response must reflect the new value")
	})

	t.Run("config entry is readable with admin role (GET /api/v1/config/site.title)", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet,
			base+"/api/v1/config/site.title",
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+newAdminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"GET /config/site.title with admin token must return 200 OK")

		var envelope struct {
			Data struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.Equal(t, "site.title", envelope.Data.Key)
		assert.Equal(t, "GoCell Updated", envelope.Data.Value,
			"GET must reflect the value written by PUT")
	})

	// PR-CFG-C negative regression: an authenticated non-admin caller is
	// rejected with 403 (not 401, not 200) on every config/flags read route.
	// The earlier alice-token captured at login was rotated by the refresh
	// step and then revoked by logout, so we re-login alice here to obtain
	// a fresh non-admin access token specifically for this assertion.
	t.Run("non-admin reader is forbidden on /api/v1/config and /api/v1/flags", func(t *testing.T) {
		loginBody := `{"username":"alice","password":"P@ssw0rd123"}`
		loginResp, err := http.Post(base+"/api/v1/access/sessions/login", //nolint:noctx
			"application/json", strings.NewReader(loginBody))
		require.NoError(t, err)
		defer loginResp.Body.Close()
		require.Equalf(t, http.StatusCreated, loginResp.StatusCode,
			"alice re-login must return 201 (PR-CFG-C 403 probe needs a live non-admin token)")

		var loginEnvelope struct {
			Data struct {
				AccessToken string `json:"accessToken"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(loginResp.Body).Decode(&loginEnvelope))
		nonAdminToken := loginEnvelope.Data.AccessToken
		require.NotEmpty(t, nonAdminToken, "alice re-login response must contain accessToken")

		paths := []string{
			"/api/v1/config/",
			"/api/v1/config/site.title",
			"/api/v1/flags/",
		}
		for _, p := range paths {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+p, http.NoBody)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+nonAdminToken)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			assert.Equalf(t, http.StatusForbidden, resp.StatusCode,
				"GET %s with non-admin token must return 403 Forbidden (not 401/200), body=%s", p, body)
		}
	})

	t.Run("feature flags list is accessible with admin role (GET /api/v1/flags)", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet,
			base+"/api/v1/flags/",
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+newAdminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"GET /flags/ with admin token must return 200 OK")

		// Response shape: {"data":[...],"nextCursor":"...","hasMore":false}
		var envelope struct {
			Data []json.RawMessage `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		// In-memory store starts empty; just verify the list shape is correct.
		assert.NotNil(t, envelope.Data,
			"flags list response must contain a 'data' array (may be empty)")
	})
}

// fetchAuditEntries queries GET /audit/entries with the given bearer token and
// returns the data array. Returns (nil, false) on any error or empty result.
func fetchAuditEntries(url, token string) ([]json.RawMessage, bool) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, false
	}
	defer resp.Body.Close()
	var page struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, false
	}
	return page.Data, len(page.Data) > 0
}
