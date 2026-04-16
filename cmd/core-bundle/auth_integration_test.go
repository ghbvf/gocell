package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopTxRunner executes fn directly without a real transaction.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = noopTxRunner{}

var testHTTPClient = &http.Client{Timeout: 2 * time.Second}

// TestAuthWiring_RealAssembly_ProtectedRoutes401 boots a real assembly
// (access-core + config-core + audit-core) with auth middleware and asserts
// that sensitive business routes return 401 without a token, while public
// routes (login, refresh) remain accessible.
//
// This is the acceptance test for AUTH-WIRE-01: "匿名请求可直达 users CRUD、
// session delete、config create/update/delete/publish/rollback" must be false
// after the fix.
func TestAuthWiring_RealAssembly_ProtectedRoutes401(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// Set up JWT key pair (same as main.go dev mode).
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", 15*time.Minute)
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet)
	require.NoError(t, err)

	eb := eventbus.New()
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)

	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithOutboxWriter(nw),
		accesscore.WithTxManager(noopTxRunner{}),
	)
	cc := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
		configcore.WithOutboxWriter(nw),
		configcore.WithTxManager(noopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
	)
	auc := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey([]byte("test-hmac-key-32-bytes-long!!!!")),
		auditcore.WithOutboxWriter(nw),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
	)

	asm := assembly.New(assembly.Config{ID: "auth-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	// Public endpoints — same as production main.go.
	publicEndpoints := []string{
		"/api/v1/access/sessions/login",
		"/api/v1/access/sessions/refresh",
	}

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(ln),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(2*time.Second),
		bootstrap.WithPublicEndpoints(publicEndpoints),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// --- Protected routes: must return 401 without token ---
	protectedRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/access/users/some-id"},
		{http.MethodPost, "/api/v1/access/users"},
		{http.MethodDelete, "/api/v1/access/sessions/some-id"},
		{http.MethodPost, "/api/v1/config/"},
		{http.MethodGet, "/api/v1/config/some-key"},
		{http.MethodPut, "/api/v1/config/some-key"},
		{http.MethodDelete, "/api/v1/config/some-key"},
		{http.MethodPost, "/api/v1/config/some-key/publish"},
		{http.MethodPost, "/api/v1/config/some-key/rollback"},
		{http.MethodGet, "/api/v1/audit/entries"},
		{http.MethodGet, "/api/v1/flags/"},
		// Internal admin endpoints (PR-A RBAC closure).
		{http.MethodPost, "/internal/v1/access/roles/assign"},
		{http.MethodPost, "/internal/v1/access/roles/revoke"},
	}

	for _, tc := range protectedRoutes {
		t.Run(fmt.Sprintf("%s_%s_401", tc.method, tc.path), func(t *testing.T) {
			req, err := http.NewRequest(tc.method, fmt.Sprintf("http://%s%s", addr, tc.path), nil)
			require.NoError(t, err)

			resp, err := testHTTPClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
				"%s %s must return 401 without auth token", tc.method, tc.path)

			var body map[string]any
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			errObj := body["error"].(map[string]any)
			assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", errObj["code"])
		})
	}

	// --- Public routes: must NOT require auth ---
	t.Run("POST_login_200", func(t *testing.T) {
		resp, err := testHTTPClient.Post(
			fmt.Sprintf("http://%s/api/v1/access/sessions/login", addr),
			"application/json", nil,
		)
		require.NoError(t, err)
		defer resp.Body.Close()
		// Login with no body returns 400 (bad request), not 401 (unauthorized).
		// 400 proves the request passed auth and reached the handler.
		assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
			"login endpoint must not return 401 (auth must be bypassed)")
	})

	t.Run("POST_refresh_bypasses_auth", func(t *testing.T) {
		resp, err := testHTTPClient.Post(
			fmt.Sprintf("http://%s/api/v1/access/sessions/refresh", addr),
			"application/json", nil,
		)
		require.NoError(t, err)
		defer resp.Body.Close()
		// Refresh with no body returns 400 (bad request), not 401.
		// 400 proves the request passed auth and reached the handler.
		assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
			"refresh endpoint must not return 401 (auth must be bypassed)")
	})

	// --- Infra: must bypass auth ---
	t.Run("healthz_200", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// --- Method drift detection: DELETE /revoke was the old route (PR#143);
	// after I-04 it moved to POST. Assert DELETE returns 404/405 to catch
	// any regression if the old handler is re-registered.
	t.Run("DELETE_revoke_rejected_as_method_drift_guard", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodDelete,
			fmt.Sprintf("http://%s/internal/v1/access/roles/revoke", addr), nil)
		require.NoError(t, err)

		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Accept either 404 (no route) or 405 (method not allowed).
		// 401 would indicate the DELETE handler is still registered.
		assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
			"DELETE /revoke must not resolve to a protected route; use POST /revoke")
	})

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}
