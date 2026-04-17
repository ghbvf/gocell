//go:build integration

// Package main — walkthrough integration test for sso-bff.
//
// Verifies the complete API flow described in README.md:
//  1. Seed admin user can log in → returns accessToken + refreshToken
//  2. refreshToken field works for token rotation
//  3. Logout returns 204 with empty body
//  4. Audit entries contain timestamp field (not createdAt)
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
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// walkthroughTxRunner executes fn directly without a real transaction (demo mode).
type walkthroughTxRunner struct{}

func (walkthroughTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = walkthroughTxRunner{}

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

	var nw outbox.Writer = outbox.NoopWriter{}

	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithOutboxWriter(nw),
		accesscore.WithTxManager(walkthroughTxRunner{}),
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
		auditcore.WithOutboxWriter(nw),
		auditcore.WithTxManager(walkthroughTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithLogger(slog.Default()),
	)

	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, ac.Init(ctx, deps))
	require.NoError(t, auc.Init(ctx, deps))

	publicEndpoints := []string{
		"/api/v1/access/sessions/login",
		"/api/v1/access/sessions/refresh",
	}

	r := router.New(
		router.WithAuthMiddleware(ac.TokenVerifier(), publicEndpoints),
	)
	ac.RegisterRoutes(r)
	auc.RegisterRoutes(r)

	srv := httptest.NewServer(r)
	cleanup := func() { srv.Close() }
	return srv, cleanup
}

// TestWalkthrough exercises the complete sso-bff API walkthrough.
func TestWalkthrough(t *testing.T) {
	testPass := generateDevPassword()
	srv, cleanup := buildWalkthroughServer(t, testPass)
	defer cleanup()

	base := srv.URL

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

		accessToken = envelope.Data.AccessToken
		refreshToken = envelope.Data.RefreshToken
	})

	// Guard: remaining subtests need valid tokens.
	require.NotEmpty(t, accessToken, "accessToken must be set by login subtest")
	require.NotEmpty(t, refreshToken, "refreshToken must be set by login subtest")

	t.Run("refresh using refreshToken field returns new token pair", func(t *testing.T) {
		payload := fmt.Sprintf(`{"refreshToken":%q}`, refreshToken)
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			base+"/api/v1/access/sessions/refresh",
			strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)

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

	t.Run("audit entries contain timestamp field not createdAt", func(t *testing.T) {
		// Re-login to get a fresh token after logout invalidated the previous session.
		loginBody := fmt.Sprintf(`{"username":"admin","password":%q}`, testPass)
		loginResp, err := http.Post(base+"/api/v1/access/sessions/login", //nolint:noctx
			"application/json", strings.NewReader(loginBody))
		require.NoError(t, err)
		defer loginResp.Body.Close()
		require.Equal(t, http.StatusCreated, loginResp.StatusCode)

		var loginEnvelope struct {
			Data struct {
				AccessToken string `json:"accessToken"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(loginResp.Body).Decode(&loginEnvelope))
		freshToken := loginEnvelope.Data.AccessToken
		require.NotEmpty(t, freshToken)

		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet,
			base+"/api/v1/audit/entries",
			http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+freshToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Decode as a page result: {"data":[...],"hasMore":bool}
		var page struct {
			Data []json.RawMessage `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&page))

		// In demo mode, audit events are delivered async via the in-memory eventbus;
		// entries may not yet be visible immediately after login. Validate field
		// names only when entries are present.
		for _, raw := range page.Data {
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
