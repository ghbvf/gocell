package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
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
// (accesscore + configcore + auditcore) with auth middleware and asserts
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
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("gocell"))
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

	// F3: public routes (login, refresh) are declared via auth.Declare(Public:true)
	// inside accesscore's RegisterRoutes. WithAuthDiscovery discovers the verifier.
	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPrimaryListener(ln), bootstrap.WithInternalListener(newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(2*time.Second),
		bootstrap.WithAuthDiscovery(),
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
		// PR-A14a: /internal/v1/* routes no longer live on the primary
		// listener; they are asserted separately below as a 404 contract.
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

	// PR-A14a: primary listener must 404 /internal/v1/* (port-level isolation).
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/internal/v1/access/roles/assign"},
		{http.MethodPost, "/internal/v1/access/roles/revoke"},
	} {
		t.Run(fmt.Sprintf("primary_404s_%s_%s", tc.method, tc.path), func(t *testing.T) {
			req, err := http.NewRequest(tc.method, fmt.Sprintf("http://%s%s", addr, tc.path), nil)
			require.NoError(t, err)
			resp, err := testHTTPClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusNotFound, resp.StatusCode,
				"primary listener must 404 %s %s (PR-A14a port-level isolation)", tc.method, tc.path)
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

	// NOTE: No runtime drift guard for DELETE /revoke. Auth middleware runs
	// before route dispatch, so an unauthenticated DELETE returns 401
	// regardless of whether the route is registered — the same status an
	// unregistered route would also produce. The drift guard is covered by
	// the positive assertion above (POST /internal/v1/access/roles/revoke
	// returns 401), which proves POST is the registered handler.

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestAuthWiring_InternalGuard_RequiresServiceToken verifies that
// /internal/v1/* endpoints are protected by the ServiceTokenMiddleware guard
// when wired via bootstrap.WithInternalEndpointGuard.
//
// Chain order: JWT auth middleware → InternalGuard → handler.
// The guard is the inner protection layer for the /internal/v1/* prefix.
//
// Test assertions:
//   - Request without Authorization → 401 from guard.
//   - Request with valid service token → guard passes (handler may return 400/404).
//   - Request to /api/v1/* with no token → 401 from JWT auth (guard not involved).
func TestAuthWiring_InternalGuard_RequiresServiceToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// JWT key pair for auth middleware.
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "guard-test", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	// Service HMAC key ring for the internal guard.
	serviceSecret := freshTestServiceSecret(t)
	ring, err := auth.NewHMACKeyRing([]byte(serviceSecret), nil)
	require.NoError(t, err)
	// A replay-safe nonce store is mandatory on every prod-equivalent wiring
	// of ServiceTokenMiddleware; matches the internalGuardFromEnv default.
	nonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenMaxAge + nonceStoreBuffer)
	require.NoError(t, err)
	guard := auth.ServiceTokenMiddleware(ring, auth.WithServiceTokenNonceStore(nonceStore))

	eb := eventbus.New()
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("guard-test-audit-key-32-bytes!!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("guard-test-config-key-32bytes!!!"))
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
		auditcore.WithHMACKey([]byte("guard-test-hmac-key-32-bytes!!!!!")),
		auditcore.WithOutboxWriter(nw),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
	)

	asm := assembly.New(assembly.Config{ID: "guard-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	// PR-A14a: /internal/v1/* endpoints live on a physically separate internal
	// listener. JWT AuthMiddleware is never installed on the internal mux —
	// WithInternalMiddleware(guard) is the sole authentication layer for the
	// control plane. F3: public routes (login, refresh) are declared by
	// accesscore via auth.Declare(Public:true); WithAuthDiscovery discovers
	// the verifier and wires it onto the primary mux only.
	internalLn := newCorebundleLocalListener(t)
	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPrimaryListener(ln),
		bootstrap.WithInternalListener(internalLn),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(2*time.Second),
		bootstrap.WithAuthDiscovery(),
		bootstrap.WithInternalMiddleware(guard),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	addr := ln.Addr().String()
	internalAddr := internalLn.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// PR-A14a primary isolation: primary listener must 404 any /internal/v1/*
	// request — those routes never reach the public mux.
	t.Run("primary_404s_internal_prefix", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/internal/v1/access/roles/assign", addr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode,
			"primary listener must 404 /internal/v1/* (port-level isolation)")
	})

	// Internal listener + /internal/v1/* without service token → 401 from guard.
	t.Run("internal_without_service_token_401", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("http://%s/internal/v1/access/roles/assign", internalAddr), nil)
		require.NoError(t, err)
		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"/internal/v1/* without service token must return 401 from guard")
	})

	// Internal listener + service token → guard passes → policy passes →
	// handler returns 404 ERR_AUTH_ROLE_NOT_FOUND (role not seeded).
	t.Run("internal_assign_service_token_policy_passes_to_handler", func(t *testing.T) {
		body := strings.NewReader(`{"userId":"usr-2","roleId":"nonexistent"}`)
		token := auth.GenerateServiceToken(ring,
			http.MethodPost, "/internal/v1/access/roles/assign", "", time.Now())
		require.NotEmpty(t, token)

		req, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("http://%s/internal/v1/access/roles/assign", internalAddr), body)
		require.NoError(t, err)
		req.Header.Set("Authorization", "ServiceToken "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode,
			"expected guard+policy to pass and handler to return 404 (role not found); body=%s", bodyBytes)
		assert.Contains(t, string(bodyBytes), "ERR_AUTH_ROLE_NOT_FOUND",
			"response must contain role-not-found error code; body=%s", bodyBytes)
	})

	// Internal listener + service token → guard passes → policy passes →
	// handler returns 200 (idempotent revoke of unassigned role).
	t.Run("internal_revoke_service_token_policy_passes_to_handler", func(t *testing.T) {
		body := strings.NewReader(`{"userId":"usr-2","roleId":"nonexistent"}`)
		token := auth.GenerateServiceToken(ring,
			http.MethodPost, "/internal/v1/access/roles/revoke", "", time.Now())
		require.NotEmpty(t, token)

		req, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("http://%s/internal/v1/access/roles/revoke", internalAddr), body)
		require.NoError(t, err)
		req.Header.Set("Authorization", "ServiceToken "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"expected guard+policy to pass and handler to return 200 (idempotent revoke); body=%s", bodyBytes)
		assert.Contains(t, string(bodyBytes), `"revoked"`,
			"response must contain revoke result; body=%s", bodyBytes)
	})

	// Internal listener + replay of the same service token → first 200 from
	// handler, second 401 from guard (nonce store rejects the replay). Exercises
	// the S-nonce P1 closure across the full HTTP stack: token signing, HMAC
	// verification, nonce CheckAndMark, JSON error envelope formatting.
	t.Run("internal_replay_same_nonce_401", func(t *testing.T) {
		token := auth.GenerateServiceToken(ring,
			http.MethodPost, "/internal/v1/access/roles/revoke", "", time.Now())
		require.NotEmpty(t, token)

		doReq := func() (int, string) {
			body := strings.NewReader(`{"userId":"usr-2","roleId":"nonexistent"}`)
			req, err := http.NewRequest(http.MethodPost,
				fmt.Sprintf("http://%s/internal/v1/access/roles/revoke", internalAddr), body)
			require.NoError(t, err)
			req.Header.Set("Authorization", "ServiceToken "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := testHTTPClient.Do(req)
			require.NoError(t, err)
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return resp.StatusCode, string(bodyBytes)
		}

		status1, _ := doReq()
		assert.Equal(t, http.StatusOK, status1,
			"first use of valid token must pass through the guard")

		status2, body2 := doReq()
		assert.Equal(t, http.StatusUnauthorized, status2,
			"replay of same nonce within TTL must be rejected by the guard")
		assert.Contains(t, body2, "ERR_AUTH_UNAUTHORIZED",
			"replay response must carry the auth error code; body=%s", body2)
	})

	// /api/v1/* on primary without token → 401 from JWT auth (guard not involved).
	t.Run("api_without_token_401_from_jwt_auth", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf("http://%s/api/v1/access/users/x", addr), nil)
		require.NoError(t, err)
		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"/api/v1/* without JWT must return 401 from auth middleware")
	})

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}
