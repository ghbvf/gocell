// Package router — per-listener router tests (PR-A14b).
// Replaces the old dual-mux tests (PR-A14a) with per-listener Router semantics.
// Each Router now wraps a SINGLE chi.Mux root for ONE listener; bootstrap
// builds one Router per declared listener and applies its default Policy.
package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// dualMuxMockVerifier implements kauth.IntentTokenVerifier for per-listener tests.
type dualMuxMockVerifier struct {
	claims kauth.Claims
	err    error
	called atomic.Int64
}

func (v *dualMuxMockVerifier) VerifyIntent(_ context.Context, _ string, _ kauth.TokenIntent) (kauth.Claims, error) {
	v.called.Add(1)
	return v.claims, v.err
}

// countingMW returns a middleware that increments counter then calls next.
func countingMW(counter *atomic.Int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

// TestPerListener_PrimaryRouter_Returns404_ForUnregisteredPaths verifies that
// the primary listener router (built via NewForListener) returns 404 for any
// path not registered, including /internal/v1/* and /healthz.
func TestPerListener_PrimaryRouter_Returns404_ForUnregisteredPaths(t *testing.T) {
	rtr, err := NewForListener(clock.Real(), kcell.PrimaryListener)
	require.NoError(t, err)

	cases := []string{
		"/internal/v1/foo",
		"/internal/v1/",
		"/healthz",
		"/readyz",
		"/metrics",
	}
	for _, p := range cases {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		rtr.Handler().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code,
			"primary listener router must 404 on %q — routes only what cells register", p)
	}
}

// TestPerListener_InternalRouter_RoutesInternalPrefix verifies that a route
// registered on an InternalListener router is reachable through that router.
func TestPerListener_InternalRouter_RoutesInternalPrefix(t *testing.T) {
	rtr, err := NewForListener(clock.Real(), kcell.InternalListener)
	require.NoError(t, err)

	var hit atomic.Int64
	rtr.Route("/internal/v1/access", func(sub kcell.RouteMux) {
		sub.Handle("/roles", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hit.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
	})

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/access/roles", nil)
	rec := httptest.NewRecorder()
	rtr.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int64(1), hit.Load())
}

// TestPerListener_HealthRouter_RoutesHealthPrefix verifies a health-listener
// router serves health paths.
func TestPerListener_HealthRouter_RoutesHealthPrefix(t *testing.T) {
	rtr, err := NewForListener(clock.Real(), kcell.HealthListener)
	require.NoError(t, err)

	var hit atomic.Int64
	rtr.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	rtr.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int64(1), hit.Load())
}

// TestPerListener_Middleware_AppliedToSingleMux verifies that middleware added
// via With() is invoked for routes on that router's single mux.
func TestPerListener_Middleware_AppliedToSingleMux(t *testing.T) {
	var guardCount atomic.Int64
	guard := countingMW(&guardCount)

	rtr, err := NewForListener(clock.Real(), kcell.InternalListener)
	require.NoError(t, err)

	rtr.Route("/internal/v1/access", func(sub kcell.RouteMux) {
		protected := sub.With(guard)
		protected.Handle("/roles", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/access/roles", nil)
	rec := httptest.NewRecorder()
	rtr.Handler().ServeHTTP(rec, req)
	assert.Equal(t, int64(1), guardCount.Load(), "middleware must fire for registered route")
}

// TestPerListener_InternalRoutes_WithAuthMiddleware_Enforces verifies that JWT
// auth is NOT installed on an InternalListener router unless WithAuthMiddleware
// is explicitly passed. Policy enforcement is at the listener level via
// PolicyServiceToken / PolicyMTLS, not via WithAuthMiddleware.
func TestPerListener_InternalRoutes_NoDefaultAuth(t *testing.T) {
	rtr, err := NewForListener(clock.Real(), kcell.InternalListener) // no policy, no auth middleware
	require.NoError(t, err)

	var reached atomic.Int64
	rtr.Route("/internal/v1/x", func(sub kcell.RouteMux) {
		sub.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
	})

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/x/", nil)
	rec := httptest.NewRecorder()
	rtr.Handler().ServeHTTP(rec, req)

	assert.Equal(t, int64(1), reached.Load(), "handler must be reached on internal router without auth")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestPerListener_PrimaryRouter_WithAuthMiddleware_Enforces verifies JWT auth
// on a primary listener router.
func TestPerListener_PrimaryRouter_WithAuthMiddleware_Enforces(t *testing.T) {
	verifier := &dualMuxMockVerifier{err: errors.New("no token provided")}

	rtr, err := NewForListener(
		clock.Real(), kcell.PrimaryListener,
		WithAuthMiddleware(verifier),
		// Whitelist the test path from policy coverage; this test validates JWT
		// enforcement, not auth.Declare coverage.
		WithPolicyCoverageWhitelist([]string{"/api/v1/foo/*"}),
	)
	require.NoError(t, err)

	rtr.Route("/api/v1/foo", func(sub kcell.RouteMux) {
		sub.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})
	require.NoError(t, rtr.FinalizeAuth())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/foo/", nil)
	rec := httptest.NewRecorder()
	rtr.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"/api/v1/* without JWT must return 401 on PrimaryListener router")
}

func TestPerListener_PrimaryRouter_BodyLimitRejectsBeforeAuth(t *testing.T) {
	verifier := &dualMuxMockVerifier{
		claims: kauth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	rtr, err := NewForListener(
		clock.Real(), kcell.PrimaryListener,
		WithBodyLimit(4),
		WithAuthMiddleware(verifier),
	)
	require.NoError(t, err)

	rtr.Handle("POST /api/v1/protected", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run when body limit rejects")
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/protected", strings.NewReader("too-large"))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.ContentLength = int64(len("too-large"))
	rec := httptest.NewRecorder()
	rtr.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, int64(0), verifier.called.Load(), "BodyLimit must reject before JWT auth verifies the bearer token")
}

func TestPerListener_InternalRouter_BodyLimitRejectsBeforeDefaultMiddleware(t *testing.T) {
	var defaultAuthCalls atomic.Int64
	rtr, err := NewForListener(
		clock.Real(), kcell.InternalListener,
		WithBodyLimit(4),
		WithDefaultMiddleware(countingMW(&defaultAuthCalls)),
	)
	require.NoError(t, err)

	rtr.Handle("POST /internal/v1/protected", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run when body limit rejects")
	}))

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/protected", strings.NewReader("too-large"))
	req.ContentLength = int64(len("too-large"))
	rec := httptest.NewRecorder()
	rtr.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, int64(0), defaultAuthCalls.Load(), "BodyLimit must reject before listener-level auth/default middleware")
}

func TestPerListener_PrimaryRouter_PublicRoute_BodyLimitStillApplies(t *testing.T) {
	verifier := &dualMuxMockVerifier{
		claims: kauth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	rtr, err := NewForListener(
		clock.Real(), kcell.PrimaryListener,
		WithBodyLimit(4),
		WithAuthMiddleware(verifier),
	)
	require.NoError(t, err)

	rtr.Handle("POST /api/v1/public", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run when body limit rejects")
	}))
	require.NoError(t, rtr.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method: http.MethodPost,
		Path:   "/api/v1/public",
		Public: true,
	}))
	require.NoError(t, rtr.FinalizeAuth())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/public", strings.NewReader("too-large"))
	req.ContentLength = int64(len("too-large"))
	rec := httptest.NewRecorder()
	rtr.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, int64(0), verifier.called.Load(), "public auth bypass must not bypass BodyLimit")
}

// TestDualMux_FinalizeAuth_InternalPathOnZeroRefAccepted verifies that a
// /internal/v1/* route declared on a zero-ref router (unit-test scenario)
// passes FinalizeAuth — the listener-identity check is skipped for zero-ref.
func TestDualMux_FinalizeAuth_InternalPathOnZeroRefAccepted(t *testing.T) {
	rtr, err := New(clock.Real())
	require.NoError(t, err)

	// Zero-ref router: listener-identity check is skipped.
	require.NoError(t, rtr.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method: http.MethodPost, Path: "/internal/v1/roles",
	}))

	err = rtr.FinalizeAuth()
	// Zero-ref router should not fail on internal-path routes.
	assert.NoError(t, err)
}

// TestDualMux_FinalizeAuth_AcceptsConsistentDeclarations confirms the happy
// path: internal /internal/v1/* and public /api/v1/* routes both pass.
func TestDualMux_FinalizeAuth_AcceptsConsistentDeclarations(t *testing.T) {
	rtr, err := New(clock.Real(), WithPolicyCoverageWhitelist([]string{
		"/internal/v1/*",
		"/api/v1/*",
	}))
	require.NoError(t, err)

	require.NoError(t, rtr.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method: http.MethodGet, Path: "/internal/v1/access/roles",
	}))
	require.NoError(t, rtr.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method: http.MethodGet, Path: "/api/v1/foo", Public: true,
	}))

	err = rtr.FinalizeAuth()
	assert.NoError(t, err)
}

// TestInternalPrefixIsolationResponder verifies the early-responder
// middleware 404's /internal/v1/* on a PrimaryListener router BEFORE any
// auth or policy runs (PR-258 RES-5 narrowing — replaces the prior chi-
// route + public-matcher-prefix + policy-coverage-whitelist mechanism).
func TestInternalPrefixIsolationResponder(t *testing.T) {
	rtr, err := NewForListener(clock.Real(), kcell.PrimaryListener,
		InternalPrefixIsolationResponder())
	require.NoError(t, err)

	for _, p := range []string{"/internal/v1", "/internal/v1/", "/internal/v1/anything"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		rtr.Handler().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code,
			"primary handler must 404 on %q (PR-A14b isolation, RES-5 middleware-based)", p)
	}
}

// TestAdminPrefixIsolationResponder verifies the admin counterpart (#1505):
// /admin/v1/* probes to the primary listener 404 before auth, so the public port
// never reveals operator endpoints (symmetric with the internal isolation).
func TestAdminPrefixIsolationResponder(t *testing.T) {
	rtr, err := NewForListener(clock.Real(), kcell.PrimaryListener,
		AdminPrefixIsolationResponder())
	require.NoError(t, err)

	for _, p := range []string{"/admin/v1", "/admin/v1/", "/admin/v1/projection/ordercell/orders/rebuild"} {
		req := httptest.NewRequest(http.MethodPost, p, nil)
		rec := httptest.NewRecorder()
		rtr.Handler().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code,
			"primary handler must 404 on %q (admin port-level isolation, #1505)", p)
	}
}
