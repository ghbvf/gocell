//go:build integration

// Package main — walkthrough integration test for sso-bff.
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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/eventrouter"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/worker"
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

// buildWalkthroughServer constructs an in-memory test server that mirrors
// sso-bff main.go but uses httptest.NewServer for port-free testing.
//
// It uses WithInitialAdminBootstrap so bootstrap credentials are written to
// <stateDir>/initial_admin_password. Callers read credentials from the file.
//
// The capturing slog handler is threaded through so the test can assert that
// no plaintext password appears in any log record (PR#172 F1 regression gate).
func buildWalkthroughServer(t *testing.T, stateDir string, capHandler *capturingHandler) (*httptest.Server, func()) {
	t.Helper()

	// Set GOCELL_STATE_DIR so the bootstrapper resolves the credential path to
	// <stateDir>/initial_admin_password.
	t.Setenv("GOCELL_STATE_DIR", stateDir)

	testLogger := slog.New(capHandler)

	eb := eventbus.New()
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)

	jwtIssuer, err := auth.NewJWTIssuer(keySet, "sso-bff-test", 15*time.Minute)
	require.NoError(t, err)

	jwtVerifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences(auth.DefaultJWTAudience))
	require.NoError(t, err)

	// The bootstrap sink captures the cleaner worker. In tests the cleaner
	// lifecycle does not need to run — the temp dir is cleaned up by t.Cleanup.
	var _ worker.Worker // ensure the import is used
	bootstrapSink := func(_ worker.Worker) {} // intentional no-op in test

	// Demo mode: no outboxWriter/txRunner — cells publish directly via the
	// eventbus publisher. This ensures access-core events reach audit-core's
	// subscriber without transactional outbox machinery.
	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithLogger(testLogger),
		accesscore.WithInitialAdminBootstrap(),
		// Sink is required by WithInitialAdminBootstrap. We capture the worker
		// but do not run it — the temp dir is cleaned up by t.Cleanup.
		accesscore.WithBootstrapWorkerSink(bootstrapSink),
	)

	auditHMACKey := []byte("walkthrough-test-hmac-key-32b!!!")
	auditCursorCodec, err := query.NewCursorCodec([]byte("walkthrough-audit-cursor-key-32b"))
	require.NoError(t, err)

	auc := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(auditHMACKey),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithLogger(testLogger),
	)

	configCursorCodec, err := query.NewCursorCodec([]byte("walkthrough-config-cursor-key-32b"))
	require.NoError(t, err)

	// config-core: demo mode — publisher only, no outboxWriter/txRunner.
	cc := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithLogger(testLogger),
	)

	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, ac.Init(ctx, deps))
	require.NoError(t, auc.Init(ctx, deps))
	require.NoError(t, cc.Init(ctx, deps))

	publicEndpoints := []string{
		"POST /api/v1/access/sessions/login",
		"POST /api/v1/access/sessions/refresh",
	}

	r := router.New(
		router.WithPublicEndpoints(publicEndpoints),
		router.WithAuthMiddleware(ac.TokenVerifier(), nil),
	)
	ac.RegisterRoutes(r)
	auc.RegisterRoutes(r)
	cc.RegisterRoutes(r)

	// Wire audit-core event subscriptions so access-core events reach the
	// audit handler asynchronously (mirrors bootstrap wiring in main.go).
	evtRouter := eventrouter.New(eb)
	require.NoError(t, auc.RegisterSubscriptions(evtRouter))
	require.NoError(t, cc.RegisterSubscriptions(evtRouter))

	evtCtx, evtCancel := context.WithCancel(context.Background())
	evtDone := make(chan struct{})
	go func() {
		defer close(evtDone)
		_ = evtRouter.Run(evtCtx)
	}()
	// Wait briefly for subscriptions to start consuming.
	select {
	case <-evtRouter.Running():
	case <-time.After(500 * time.Millisecond):
	}

	srv := httptest.NewServer(r)
	cleanup := func() {
		srv.Close()
		evtCancel()
		<-evtDone
	}
	return srv, cleanup
}

// TestWalkthrough exercises the complete sso-bff API walkthrough.
func TestWalkthrough(t *testing.T) {
	// Use a temp dir as GOCELL_STATE_DIR so the credential file is isolated
	// to this test run. The slog capturing handler wraps a discard handler
	// so we can assert no plaintext password appears in logs.
	stateDir := t.TempDir()
	capHandler := newCapturingHandler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, cleanup := buildWalkthroughServer(t, stateDir, capHandler)
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

	base := srv.URL

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
				PasswordResetRequired bool   `json:"passwordResetRequired"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.NotEmpty(t, envelope.Data.AccessToken, "response must include accessToken")
		assert.NotEmpty(t, envelope.Data.RefreshToken, "response must include refreshToken")
		assert.NotEmpty(t, envelope.Data.ExpiresAt, "response must include expiresAt")
		assert.True(t, envelope.Data.PasswordResetRequired,
			"bootstrap admin login must return passwordResetRequired=true")

		adminToken = envelope.Data.AccessToken
	})

	// Guard: remaining subtests need a valid admin token.
	require.NotEmpty(t, adminToken, "adminToken must be set by admin login subtest")

	// Extract admin user ID from the JWT subject claim.
	adminUserID = extractSubjectFromJWT(t, adminToken)
	require.NotEmpty(t, adminUserID, "admin user ID must be extractable from JWT sub claim")

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
				PasswordResetRequired bool   `json:"passwordResetRequired"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(bodyBytes, &envelope))
		assert.NotEmpty(t, envelope.Data.AccessToken, "response must include new accessToken")
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

	// Steps 8-11: config-core CRUD + feature flags.
	// Write ops require admin role; read ops are open (no auth required).

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

	t.Run("config entry is readable with valid JWT (GET /api/v1/config/site.title)", func(t *testing.T) {
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
			"GET /config/site.title with valid JWT must return 200 OK")

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

	t.Run("feature flags list is accessible with valid JWT (GET /api/v1/flags)", func(t *testing.T) {
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
			"GET /flags/ with valid JWT must return 200 OK")

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

// extractSubjectFromJWT extracts the "sub" claim from a JWT without verifying
// the signature. Used in tests only to retrieve the user ID for change-password.
//
// The JWT payload is base64url-encoded between the first and second ".".
func extractSubjectFromJWT(t *testing.T, tokenStr string) string {
	t.Helper()
	parts := strings.SplitN(tokenStr, ".", 3)
	require.Len(t, parts, 3, "JWT must have 3 parts")

	// base64.RawURLEncoding handles base64url without padding.
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err, "JWT payload must be valid base64url")

	var claims map[string]any
	require.NoError(t, json.Unmarshal(decoded, &claims))

	sub, _ := claims["sub"].(string)
	return sub
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
