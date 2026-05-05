//go:build integration

// Package main — walkthrough integration test for ssobff.
//
// Verifies the complete API flow described in README.md:
//  1. Operator provisions the first admin (POST /api/v1/access/setup/admin
//     with package-local ssobffBootstrap{Username,Password} as Basic Auth).
//     The admin chose the password directly — passwordResetRequired=false
//     (D2 + D4 (operator credential + plane separation): operator-set passwords are not "initial randoms",
//     identity-manage's change-password covers the reset flow separately).
//  2. Admin logs in with chosen password → 201 + passwordResetRequired=false.
//  3. Internal listener is isolated and service-token protected.
//  4. Business endpoints (config CRUD) work with the admin token directly —
//     no reset detour required.
//  5. refreshToken field works for token rotation.
//  6. Logout returns 204 with empty body.
//  7. Audit entries contain timestamp field (not createdAt).
//  8. Feature flags list is accessible with auth.
//
// Security regression gate (PR#172 F1): slog capture verifies no log record
// contains the bootstrap password in plaintext.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
)

const walkthroughServiceSecret = "walkthrough-service-token-secret-32b"

var walkthroughHTTPClient = &http.Client{Timeout: testtime.D1s}

type capturedLogRecords struct {
	mu      sync.Mutex
	records []slog.Record
}

// capturingHandler is an slog.Handler that records every log record for
// post-test inspection. It is goroutine-safe via a mutex.
type capturingHandler struct {
	store *capturedLogRecords
	inner slog.Handler
}

func newCapturingHandler(inner slog.Handler) *capturingHandler {
	return &capturingHandler{store: &capturedLogRecords{}, inner: inner}
}

func (h *capturingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *capturingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.store.mu.Lock()
	h.store.records = append(h.store.records, r.Clone())
	h.store.mu.Unlock()
	return h.inner.Handle(ctx, r)
}

func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &capturingHandler{store: h.store, inner: h.inner.WithAttrs(attrs)}
}

func (h *capturingHandler) WithGroup(name string) slog.Handler {
	return &capturingHandler{store: h.store, inner: h.inner.WithGroup(name)}
}

// assertNoPlaintextPassword verifies that none of the captured slog records
// contain the given password in any attribute value. This is the regression
// gate for PR#172 F1 (plaintext password in structured logs).
func assertNoPlaintextPassword(t *testing.T, h *capturingHandler, password string) {
	t.Helper()
	if password == "" {
		return // no password to check
	}
	h.store.mu.Lock()
	records := make([]slog.Record, len(h.store.records))
	copy(records, h.store.records)
	h.store.mu.Unlock()

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

// buildWalkthroughServer constructs an in-memory ssobff server through the
// same NewSSOBFFApp/bootstrap path used by main.go.
//
// Admin provisioning uses interactive POST /setup/admin (postmortem 202605060030:
// bootstrap provision mode deleted; operator calls the endpoint with Basic Auth).
// The capturing slog handler is threaded through so the test can assert
// that no plaintext password appears in any log record (PR#172 F1 gate).
type walkthroughServer struct {
	primaryBaseURL  string
	internalBaseURL string
	healthBaseURL   string
	serviceSecret   string
	cancel          context.CancelFunc
	done            <-chan error
}

func (s *walkthroughServer) Cleanup(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		require.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("ssobff did not shut down within 5s")
	}
}

func buildWalkthroughServer(t *testing.T, capHandler *capturingHandler) *walkthroughServer {
	t.Helper()

	logger := slog.New(capHandler)
	previousDefaultLogger := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previousDefaultLogger) })

	primary := newWalkthroughListener(t)
	internal := newWalkthroughListener(t)
	health := newWalkthroughListener(t)
	app, err := NewSSOBFFApp(
		WithSSOBFFLogger(logger),
		WithSSOBFFInternalServiceSecret(walkthroughServiceSecret),
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
	waitForWalkthroughReady(t, "http://"+app.HealthListenAddr()+"/readyz", done)

	return &walkthroughServer{
		primaryBaseURL:  "http://" + app.PrimaryListenAddr(),
		internalBaseURL: "http://" + app.InternalListenAddr(),
		healthBaseURL:   "http://" + app.HealthListenAddr(),
		serviceSecret:   walkthroughServiceSecret,
		cancel:          cancel,
		done:            done,
	}
}

func newWalkthroughListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func waitForWalkthroughReady(t *testing.T, readyzURL string, done <-chan error) {
	t.Helper()
	deadline := time.NewTimer(testtime.EventuallyLong)
	defer deadline.Stop()
	tick := time.NewTicker(testtime.MediumPoll)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			t.Fatal("ssobff exited before becoming ready")
		case <-deadline.C:
			t.Fatal("ssobff bootstrap never became ready within 5s")
		case <-tick.C:
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, readyzURL, http.NoBody)
			require.NoError(t, err)
			resp, err := walkthroughHTTPClient.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
	}
}

func doWalkthroughRequest(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := walkthroughHTTPClient.Do(req)
	require.NoError(t, err)
	return resp
}

func postWalkthroughJSON(t *testing.T, rawURL string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, rawURL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	return doWalkthroughRequest(t, req)
}

// provisionAdmin sends POST /api/v1/access/setup/admin with the package-local
// ssobffBootstrap{Username,Password} as Basic Auth, creating the demo admin.
// The closed contract (ADR §D1) guarantees the request is rejected with 401
// without Basic Auth — Step 0 of the walkthrough.
func provisionAdmin(t *testing.T, base, username, email, password string) {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"email":%q,"password":%q}`, username, email, password)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		base+"/api/v1/access/setup/admin", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(ssobffBootstrapUsername, ssobffBootstrapPassword)

	resp := doWalkthroughRequest(t, req)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"setup/admin must return 201 Created with valid Basic Auth")
}

func walkthroughServiceToken(t *testing.T, secret, method, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	require.NoError(t, err)
	// Spec: 4-part token — callerCell="accesscore" (ssobff is the accesscore BFF).
	return auth.GenerateServiceToken(ring, "accesscore", method, parsed.Path, parsed.RawQuery, time.Now())
}

// TestWalkthrough exercises the complete ssobff API walkthrough.
func TestWalkthrough(t *testing.T) {
	// The slog capturing handler wraps a discard handler so we can assert no
	// plaintext password appears in logs (PR#172 F1 regression gate). PR #392
	// removed the credfile path; bootstrap credentials are now the package-
	// local ssobffBootstrap{Username,Password} constants.
	capHandler := newCapturingHandler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := buildWalkthroughServer(t, capHandler)
	defer srv.Cleanup(t)
	require.NotEmpty(t, srv.healthBaseURL, "health base URL must be available for diagnostics")
	base := srv.primaryBaseURL

	// Step 0: Provision the first admin via interactive POST /setup/admin
	// (D5: env creds authenticate the operator; body defines the admin user).
	const (
		adminUsername     = "walkthrough-admin"
		adminEmail        = "walkthrough-admin@local"
		bootstrapPassword = "Walkthrough-Admin-Pwd-1!"
	)
	bootstrapUsername := adminUsername
	provisionAdmin(t, base, adminUsername, adminEmail, bootstrapPassword)

	var adminToken string
	var adminUserID string

	// Step 1: Operator-provisioned admin login. Operator chose the password,
	// so passwordResetRequired=false (D2 + D4 (operator credential + plane separation)). The reset flow itself is
	// covered by identity-manage's change-password tests, not by walkthrough.
	t.Run("provisioned admin can login", func(t *testing.T) {
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, bootstrapUsername, bootstrapPassword)
		resp := postWalkthroughJSON(t, base+"/api/v1/access/sessions/login", body)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode,
			"admin login must return 201 Created")

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
		assert.False(t, envelope.Data.PasswordResetRequired,
			"operator-provisioned admin must not require password reset")

		adminToken = envelope.Data.AccessToken
		adminUserID = envelope.Data.UserID
	})

	// Guard: remaining subtests need a valid admin token.
	require.NotEmpty(t, adminToken, "adminToken must be set by admin login subtest")
	require.NotEmpty(t, adminUserID, "adminUserID must be set from login response userId field")

	t.Run("internal listener is isolated and service-token protected", func(t *testing.T) {
		const internalRoleAssignPath = "/internal/v1/access/roles/assign"
		body := fmt.Sprintf(`{"userId":%q,"roleId":"admin"}`, adminUserID)

		primaryReq, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			srv.primaryBaseURL+internalRoleAssignPath,
			strings.NewReader(body))
		require.NoError(t, err)
		primaryReq.Header.Set("Content-Type", "application/json")
		primaryResp := doWalkthroughRequest(t, primaryReq)
		primaryResp.Body.Close()
		assert.Equal(t, http.StatusNotFound, primaryResp.StatusCode,
			"primary listener must not expose /internal/v1/* routes")

		internalURL := srv.internalBaseURL + internalRoleAssignPath
		unauthReq, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			internalURL,
			strings.NewReader(body))
		require.NoError(t, err)
		unauthReq.Header.Set("Content-Type", "application/json")
		unauthResp := doWalkthroughRequest(t, unauthReq)
		unauthResp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, unauthResp.StatusCode,
			"internal listener must reject missing service token")

		authReq, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			internalURL,
			strings.NewReader(body))
		require.NoError(t, err)
		authReq.Header.Set("Content-Type", "application/json")
		authReq.Header.Set("Authorization", "ServiceToken "+
			walkthroughServiceToken(t, srv.serviceSecret, http.MethodPost, internalURL))
		authResp := doWalkthroughRequest(t, authReq)
		defer authResp.Body.Close()
		assert.Equal(t, http.StatusCreated, authResp.StatusCode,
			"internal listener must accept valid service token on internal route")
	})

	// Step 2: Business endpoints work directly with the admin token (operator-
	// provisioned admin → no reset detour). Create a config entry, then read
	// it back.
	t.Run("business endpoint allowed with admin token", func(t *testing.T) {
		// Create a config entry first.
		createReq, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/config/",
			strings.NewReader(`{"key":"site.title","value":"GoCell Demo","sensitive":false}`))
		require.NoError(t, err)
		createReq.Header.Set("Content-Type", "application/json")
		createReq.Header.Set("Authorization", "Bearer "+adminToken)

		createResp := doWalkthroughRequest(t, createReq)
		createResp.Body.Close()
		assert.Equal(t, http.StatusCreated, createResp.StatusCode,
			"POST /config/ with admin token must return 201 Created")

		// Now read it back.
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet,
			base+"/api/v1/config/site.title",
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+adminToken)

		resp := doWalkthroughRequest(t, req)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"GET /config/site.title with admin token must return 200 OK")
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
		req.Header.Set("Authorization", "Bearer "+adminToken)

		resp := doWalkthroughRequest(t, req)
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
		resp := postWalkthroughJSON(t, base+"/api/v1/access/sessions/login", body)
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

		resp := doWalkthroughRequest(t, req)
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

		resp := doWalkthroughRequest(t, req)
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

		resp := doWalkthroughRequest(t, req)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusGone,
			"post-logout refresh must return 401 or 410, got %d", resp.StatusCode)
	})

	t.Run("audit entries require auth and contain timestamp field not createdAt", func(t *testing.T) {
		// In demo mode, audit events are delivered async via the in-memory
		// eventbus; poll until at least one entry is visible.
		var entries []json.RawMessage
		require.Eventually(t, func() bool {
			data, ok := fetchAuditEntries(base+"/api/v1/audit/entries", adminToken)
			if ok {
				entries = data
			}
			return ok
		}, testtime.D2s, testtime.MediumPoll, "expected at least one audit entry")

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
		req.Header.Set("Authorization", "Bearer "+adminToken)

		resp := doWalkthroughRequest(t, req)
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
		req.Header.Set("Authorization", "Bearer "+adminToken)

		resp := doWalkthroughRequest(t, req)
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
		loginResp := postWalkthroughJSON(t, base+"/api/v1/access/sessions/login", loginBody)
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

			resp := doWalkthroughRequest(t, req)
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
		req.Header.Set("Authorization", "Bearer "+adminToken)

		resp := doWalkthroughRequest(t, req)
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
	resp, err := walkthroughHTTPClient.Do(req)
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
