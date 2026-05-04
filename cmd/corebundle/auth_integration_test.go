//go:build integration

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// noopTxRunner executes fn directly without a real transaction.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = noopTxRunner{}

var testHTTPClient = &http.Client{Timeout: testtime.D2s}

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
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)

	ac := accesscore.NewAccessCore(
		accesscore.WithClock(clock.Real()),
		accesscore.WithInMemoryDefaults(),
		accesscore.WithOutboxDeps(eb, nw),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(noopTxRunner{}),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
	)
	cc := configcore.NewConfigCore(
		configcore.WithClock(clock.Real()),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(eb, nw),
		configcore.WithTxManager(noopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)
	auc := auditcore.NewAuditCore(
		auditcore.WithClock(clock.Real()),
		auditcore.WithInMemoryDefaults(),
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey([]byte("test-hmac-key-32-bytes-long!!!!")),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	asm := assembly.New(assembly.Config{ID: "auth-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	// F3: public routes (login, refresh) are declared via auth.MustMount(Public:true)
	// inside accesscore's RegisterRoutes. PolicyJWTFromAssembly is now a
	// cell.Policy (round-3 collapse): its Validate hook resolves the verifier
	// from accesscore's authProvider during phase4.
	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}, bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(testtime.D2s),
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
	}, testtime.EventuallyDefault, testtime.MediumPoll, "HTTP server did not become ready")

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
	case <-time.After(testtime.SelectShutdown):
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
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "guard-test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	// Service HMAC key ring for the internal guard.
	serviceSecret := freshTestServiceSecret(t)
	ring, err := auth.NewHMACKeyRing([]byte(serviceSecret), nil)
	require.NoError(t, err)
	// A replay-safe nonce store is mandatory on every prod-equivalent wiring
	// of ServiceTokenMiddleware; matches the internalGuardFromEnv default.
	//
	// Shared across all subtests below — do NOT add t.Parallel() to the
	// subtests without isolating the store per subtest, or the replay
	// subtest's nonce can be consumed by a peer before it runs.
	nonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)
	guard := auth.ServiceTokenMiddleware(ring, clock.Real(), auth.WithServiceTokenNonceStore(nonceStore))

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("guard-test-audit-key-32-bytes!!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("guard-test-config-key-32bytes!!!"))
	require.NoError(t, err)

	ac := accesscore.NewAccessCore(
		accesscore.WithClock(clock.Real()),
		accesscore.WithInMemoryDefaults(),
		accesscore.WithOutboxDeps(eb, nw),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(noopTxRunner{}),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
	)
	cc := configcore.NewConfigCore(
		configcore.WithClock(clock.Real()),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(eb, nw),
		configcore.WithTxManager(noopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)
	auc := auditcore.NewAuditCore(
		auditcore.WithClock(clock.Real()),
		auditcore.WithInMemoryDefaults(),
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey([]byte("guard-test-hmac-key-32-bytes!!!!!")),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	asm := assembly.New(assembly.Config{ID: "guard-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	// PR-A14b: /internal/v1/* endpoints live on a physically separate internal
	// listener. JWT AuthMiddleware is never installed on the internal mux —
	// PolicyServiceToken is the sole authentication layer for the control plane.
	// F3: public routes (login, refresh) are declared by accesscore via
	// auth.MustMount(Public:true); PolicyJWTFromAssembly is the PrimaryListener's
	// cell.Policy (round-3 collapse) and resolves the verifier lazily at phase4.
	_ = guard // guard is superseded by PolicyServiceToken below
	internalLn := newCorebundleLocalListener(t)
	internalAuthChain := []cell.ListenerAuth{cell.MustNewAuthServiceToken(nonceStore, ring)}
	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}, bootstrap.WithListenerNet(ln)),
		bootstrap.WithListener(cell.InternalListener, internalLn.Addr().String(), internalAuthChain,
			bootstrap.WithListenerNet(internalLn)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(testtime.D2s),
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
	}, testtime.EventuallyDefault, testtime.MediumPoll, "HTTP server did not become ready")

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
		// Spec: 4-part token — callerCell="accesscore" is the identity claim.
		token := auth.GenerateServiceToken(ring, "accesscore",
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
		// Spec: 4-part token — callerCell="accesscore".
		token := auth.GenerateServiceToken(ring, "accesscore",
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
		// Spec: 4-part token — callerCell="accesscore".
		token := auth.GenerateServiceToken(ring, "accesscore",
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
		assert.Contains(t, body2, "ERR_AUTH_REPLAY_DETECTED",
			"replay response must carry the replay-specific error code; body=%s", body2)
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
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestAuthWiring_HealthListener_PrimaryDoesNotServeHealthz verifies that when
// a dedicated HealthListener is declared, the primary listener no longer serves
// /healthz and /readyz (TEST-10). Those endpoints physically move to the health
// listener port. With JWT auth on primary, unregistered routes return 401 (not
// 404), which proves they are absent from the primary mux regardless of auth.
func TestAuthWiring_HealthListener_PrimaryDoesNotServeHealthz(t *testing.T) {
	primaryLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	internalLn := newCorebundleLocalListener(t)
	healthLn := newCorebundleLocalListener(t)

	// JWT key pair.
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "health-test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("health-test-audit-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("health-test-config-key-32bytes!!"))
	require.NoError(t, err)

	ac := accesscore.NewAccessCore(
		accesscore.WithClock(clock.Real()),
		accesscore.WithInMemoryDefaults(),
		accesscore.WithOutboxDeps(eb, nw),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(noopTxRunner{}),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
	)
	cc := configcore.NewConfigCore(
		configcore.WithClock(clock.Real()),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(eb, nw),
		configcore.WithTxManager(noopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)
	auc := auditcore.NewAuditCore(
		auditcore.WithClock(clock.Real()),
		auditcore.WithInMemoryDefaults(),
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey([]byte("health-test-hmac-key-32-bytes!!")),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	asm := assembly.New(assembly.Config{ID: "health-listener-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(primaryLn)),
		withCorebundleTestInternalListener(t, internalLn),
		// HealthListener declared explicitly — /healthz, /readyz move here.
		bootstrap.WithListener(cell.HealthListener, healthLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}},
			bootstrap.WithListenerNet(healthLn)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	healthAddr := healthLn.Addr().String()
	primaryAddr := primaryLn.Addr().String()

	// Wait for health listener to become ready (health endpoints live there now).
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", healthAddr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "health listener did not become ready")

	// Health listener: /healthz and /readyz must return 200.
	t.Run("health_listener_serves_healthz_200", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", healthAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"health listener must serve /healthz with 200")
	})

	t.Run("health_listener_serves_readyz_200", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", healthAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"health listener must serve /readyz with 200")
	})

	// Primary listener: /healthz must NOT return 200 when a dedicated HealthListener is
	// active. With JWT auth on primary, unregistered paths return 401 (auth challenge).
	// Either 401 or 404 proves the route is absent from primary's mux.
	t.Run("primary_does_not_serve_healthz", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", primaryAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.NotEqual(t, http.StatusOK, resp.StatusCode,
			"primary listener must not return 200 for /healthz when a dedicated HealthListener is declared; "+
				"got %d (401=auth challenge on unregistered route, 404=no route registered)", resp.StatusCode)
	})

	t.Run("primary_does_not_serve_readyz", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/readyz", primaryAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.NotEqual(t, http.StatusOK, resp.StatusCode,
			"primary listener must not return 200 for /readyz when a dedicated HealthListener is declared")
	})

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}
}
