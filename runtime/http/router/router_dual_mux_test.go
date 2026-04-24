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
	"sync/atomic"
	"testing"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dualMuxMockVerifier implements auth.IntentTokenVerifier for per-listener tests.
type dualMuxMockVerifier struct {
	claims auth.Claims
	err    error
	called atomic.Int64
}

func (v *dualMuxMockVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
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
	rtr, err := NewForListener(kcell.PrimaryListener, nil)
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
	rtr, err := NewForListener(kcell.InternalListener, nil)
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
	rtr, err := NewForListener(kcell.HealthListener, nil)
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

	rtr, err := NewForListener(kcell.InternalListener, nil)
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
	rtr, err := NewForListener(kcell.InternalListener, nil) // no policy, no auth middleware
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

	rtr, err := NewForListener(kcell.PrimaryListener, nil,
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

// TestPerListener_FinalizeAuth_RejectsDelegatedOnPublicPath verifies the
// consistency assertion: Delegated=true declared on a non-/internal/v1 path
// must fail FinalizeAuth.
func TestDualMux_FinalizeAuth_RejectsDelegatedOnPublicPath(t *testing.T) {
	rtr, err := NewE()
	require.NoError(t, err)

	rtr.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method: http.MethodPost, Path: "/api/v1/foo", Delegated: true,
	})

	err = rtr.FinalizeAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Delegated")
	assert.Contains(t, err.Error(), "/internal/v1/")
}

// TestDualMux_FinalizeAuth_RejectsInternalPathWithoutDelegated verifies the
// mirror consistency assertion: /internal/v1/* path declared without
// Delegated:true must fail FinalizeAuth.
func TestDualMux_FinalizeAuth_RejectsInternalPathWithoutDelegated(t *testing.T) {
	rtr, err := NewE()
	require.NoError(t, err)

	rtr.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method: http.MethodPost, Path: "/internal/v1/roles", Delegated: false,
	})

	err = rtr.FinalizeAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Delegated")
	assert.Contains(t, err.Error(), "/internal/v1/")
}

// TestDualMux_FinalizeAuth_AcceptsConsistentDeclarations confirms the happy
// path: Delegated:true on /internal/v1/* and Delegated:false on /api/v1/*
// both pass.
func TestDualMux_FinalizeAuth_AcceptsConsistentDeclarations(t *testing.T) {
	rtr, err := NewE(WithPolicyCoverageWhitelist([]string{
		"/internal/v1/*",
		"/api/v1/*",
	}))
	require.NoError(t, err)

	rtr.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method: http.MethodGet, Path: "/internal/v1/access/roles", Delegated: true,
	})
	rtr.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method: http.MethodGet, Path: "/api/v1/foo", Public: true,
	})

	err = rtr.FinalizeAuth()
	assert.NoError(t, err)
}

// TestInstallInternalPrefixIsolation verifies that the helper installs explicit
// 404 handlers for /internal/v1/* on a PrimaryListener router, preserving
// the physical isolation contract.
func TestInstallInternalPrefixIsolation(t *testing.T) {
	rtr, err := NewForListener(kcell.PrimaryListener, nil)
	require.NoError(t, err)

	InstallInternalPrefixIsolation(rtr)

	for _, p := range []string{"/internal/v1", "/internal/v1/", "/internal/v1/anything"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		rtr.Handler().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code,
			"primary handler must 404 on %q (PR-A14b isolation)", p)
	}
}
