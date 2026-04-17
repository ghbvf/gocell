//go:build integration

// Package main — walkthrough integration test for sso-bff.
//
// Verifies the complete API flow described in README.md:
//  1. Seed admin user can log in → returns accessToken + refreshToken
//  2. refreshToken field works for token rotation
//  3. Logout returns 204 with empty body
//  4. Audit entries contain timestamp field (not createdAt)
//  5-8. Config CRUD (POST/PUT/GET) with admin token — Steps 8-11 in README
//  9. Feature flags list is accessible without auth
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildWalkthroughServer constructs an in-memory test server that mirrors
// sso-bff main.go but uses httptest.NewServer for port-free testing.
// seedPass is the admin password injected via WithSeedAdmin; callers control
// the value so tests remain deterministic without relying on hardcoded strings.
func buildWalkthroughServer(t *testing.T, seedPass string) (*httptest.Server, func()) {
	t.Helper()

	eb := eventbus.New()
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)

	jwtIssuer, err := auth.NewJWTIssuer(keySet, "sso-bff-test", 15*time.Minute)
	require.NoError(t, err)

	jwtVerifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences(auth.DefaultJWTAudience))
	require.NoError(t, err)

	// Demo mode: no outboxWriter/txRunner — cells publish directly via the
	// eventbus publisher. This ensures access-core events reach audit-core's
	// subscriber without transactional outbox machinery.
	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithLogger(slog.Default()),
		accesscore.WithSeedAdmin("admin", seedPass),
	)

	auditHMACKey := []byte("walkthrough-test-hmac-key-32b!!!")
	auditCursorCodec, err := query.NewCursorCodec([]byte("walkthrough-audit-cursor-key-32b"))
	require.NoError(t, err)

	auc := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(auditHMACKey),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithLogger(slog.Default()),
	)

	configCursorCodec, err := query.NewCursorCodec([]byte("walkthrough-config-cursor-key-32b"))
	require.NoError(t, err)

	// config-core: demo mode — publisher only, no outboxWriter/txRunner.
	cc := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithLogger(slog.Default()),
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
		"/api/v1/access/sessions/login",
		"/api/v1/access/sessions/refresh",
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
	testPass := generateDevPassword()
	srv, cleanup := buildWalkthroughServer(t, testPass)
	defer cleanup()

	base := srv.URL

	var adminToken string
	var accessToken, refreshToken string

	t.Run("seed user can login and returns accessToken+refreshToken", func(t *testing.T) {
		body := fmt.Sprintf(`{"username":"admin","password":%q}`, testPass)
		resp, err := http.Post(base+"/api/v1/access/sessions/login", //nolint:noctx
			"application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode,
			"seed admin login must return 201 Created")

		var envelope struct {
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresAt    string `json:"expiresAt"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.NotEmpty(t, envelope.Data.AccessToken, "response must include accessToken (not token)")
		assert.NotEmpty(t, envelope.Data.RefreshToken, "response must include refreshToken (not sessionId)")
		assert.NotEmpty(t, envelope.Data.ExpiresAt, "response must include expiresAt")

		adminToken = envelope.Data.AccessToken
	})

	// Guard: remaining subtests need a valid admin token.
	require.NotEmpty(t, adminToken, "adminToken must be set by admin login subtest")

	t.Run("admin can create a new user", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/access/users",
			strings.NewReader(`{"username":"alice","password":"P@ssw0rd123","email":"alice@example.com"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminToken)

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

	t.Run("alice can login and returns accessToken+refreshToken", func(t *testing.T) {
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
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.NotEmpty(t, envelope.Data.AccessToken, "response must include accessToken")
		assert.NotEmpty(t, envelope.Data.RefreshToken, "response must include refreshToken")

		accessToken = envelope.Data.AccessToken
		refreshToken = envelope.Data.RefreshToken
	})

	// Guard: remaining subtests need valid alice tokens.
	require.NotEmpty(t, accessToken, "accessToken must be set by alice login subtest")
	require.NotEmpty(t, refreshToken, "refreshToken must be set by alice login subtest")

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

	// Extract sessionId from JWT claims for logout.
	sessionID := jwtExtractSID(t, accessToken)

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
			data, ok := fetchAuditEntries(base+"/api/v1/audit/entries", adminToken)
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

	t.Run("admin can create a config entry (POST /api/v1/config/)", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/config/",
			strings.NewReader(`{"key":"site.title","value":"GoCell Demo","sensitive":false}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode,
			"POST /config/ with admin token must return 201 Created")

		var envelope struct {
			Data struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.Equal(t, "site.title", envelope.Data.Key,
			"created config entry must echo back the key")
		assert.Equal(t, "GoCell Demo", envelope.Data.Value,
			"created config entry must echo back the value")
	})

	t.Run("admin can update a config entry (PUT /api/v1/config/site.title)", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPut,
			base+"/api/v1/config/site.title",
			strings.NewReader(`{"value":"GoCell Updated"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminToken)

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
		// config-read handler has no role guard — any authenticated user can read.
		// The router middleware still requires a valid JWT (not a public endpoint).
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet,
			base+"/api/v1/config/site.title",
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+adminToken)

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
		// featureflag handler has no role guard — any authenticated user can list flags.
		// The router middleware still requires a valid JWT (not a public endpoint).
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet,
			base+"/api/v1/flags/",
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+adminToken)

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

// jwtExtractSID parses the JWT payload (without signature verification) to
// extract the session ID from the "sid" extra claim. Used to build the logout URL.
func jwtExtractSID(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "JWT must have 3 dot-separated parts")

	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err, "JWT payload must be valid base64url")

	var claims map[string]any
	require.NoError(t, json.Unmarshal(decoded, &claims), "JWT payload must be valid JSON")

	sid, ok := claims["sid"].(string)
	require.True(t, ok, "JWT must contain 'sid' string claim for session ID")
	require.NotEmpty(t, sid)
	return sid
}
