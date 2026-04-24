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

// mockIntentVerifier is reused across dual-mux tests. Duplicated here rather
// than shared to keep each _test.go file self-contained under its top-of-file
// narrative; the tiny type is cheap to repeat.
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

// TestDualMux_PublicHandler_Returns404_ForInternalPrefix is the core assertion
// for PR-A14a: the primary (public) handler must NOT have /internal/v1/* routes
// mounted on it. Even if a Cell accidentally tries to register such a route,
// it must land on the internal mux — so probing the public handler with an
// /internal/v1/* URL yields 404, not 401 (which would signal the guard ran
// on the primary listener, defeating the isolation).
func TestDualMux_PublicHandler_Returns404_ForInternalPrefix(t *testing.T) {
	rtr, err := NewE()
	require.NoError(t, err)

	// Register a handler via Route("/internal/v1/...") — it must land on
	// internalMux, NOT publicMux. PublicHandler must return 404 for such paths.
	rtr.Route("/internal/v1/foo", func(sub kcell.RouteMux) {
		sub.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/foo/", nil)
	rec := httptest.NewRecorder()
	rtr.PublicHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code,
		"public handler must 404 on /internal/v1/* — physical listener isolation")
}

// TestDualMux_InternalHandler_Returns404_ForPublicPrefix verifies the mirror:
// the internal handler must not route /api/v1/*, /healthz, or any non-internal path.
func TestDualMux_InternalHandler_Returns404_ForPublicPrefix(t *testing.T) {
	rtr, err := NewE()
	require.NoError(t, err)

	// Register a business route on the public mux via Route("/api/v1/...").
	rtr.Route("/api/v1/foo", func(sub kcell.RouteMux) {
		sub.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	cases := []string{"/api/v1/foo/", "/healthz", "/metrics", "/readyz", "/"}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		rtr.InternalHandler().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code,
			"internal handler must 404 on non-/internal/v1/* path %q", path)
	}
}

// TestDualMux_InternalHandler_RoutesInternalPrefix verifies that a route
// registered via mux.Route("/internal/v1/...") is reachable through the
// internal handler (and ONLY through it).
func TestDualMux_InternalHandler_RoutesInternalPrefix(t *testing.T) {
	rtr, err := NewE()
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
	rtr.InternalHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int64(1), hit.Load())
}

// TestDualMux_InternalMiddleware_AppliedToInternalMuxOnly verifies that
// middleware installed via WithInternalMiddleware is invoked for /internal/v1/*
// paths on the internal handler and NOT invoked for any path on the public
// handler.
func TestDualMux_InternalMiddleware_AppliedToInternalMuxOnly(t *testing.T) {
	var guardCount atomic.Int64
	guard := countingMW(&guardCount)

	rtr, err := NewE(WithInternalMiddleware(guard))
	require.NoError(t, err)

	// Register an internal route so the internal mux can dispatch.
	rtr.Route("/internal/v1/access", func(sub kcell.RouteMux) {
		sub.Handle("/roles", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})
	// Register a public route for control.
	rtr.Route("/api/v1/foo", func(sub kcell.RouteMux) {
		sub.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	// Hit internal → guard runs.
	rec := httptest.NewRecorder()
	rtr.InternalHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/internal/v1/access/roles", nil))
	assert.Equal(t, int64(1), guardCount.Load(), "internal middleware must run for /internal/v1/*")

	// Hit public /api/v1/foo → guard must NOT run.
	rec = httptest.NewRecorder()
	rtr.PublicHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/foo/", nil))
	assert.Equal(t, int64(1), guardCount.Load(), "internal middleware must NOT run for /api/v1/*")

	// Hit public /healthz-ish path → guard must NOT run.
	rec = httptest.NewRecorder()
	rtr.PublicHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, int64(1), guardCount.Load(), "internal middleware must NOT run for /healthz")
}

// TestDualMux_InternalRoutes_NeverReachJWTMiddleware is the replacement for
// the deleted WithInternalPathPrefixGuard auto-delegation test: the new
// design guarantees /internal/v1/* is physically mounted on internalMux, which
// has NO JWT middleware installed. So a request to an internal route without
// an Authorization header reaches the handler (or the internal middleware if
// configured) — the JWT verifier is never called.
func TestDualMux_InternalRoutes_NeverReachJWTMiddleware(t *testing.T) {
	verifier := &dualMuxMockVerifier{err: errors.New("JWT must not run for internal mux")}

	rtr, err := NewE(WithAuthMiddleware(verifier))
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
	rtr.InternalHandler().ServeHTTP(rec, req)

	assert.Equal(t, int64(0), verifier.called.Load(),
		"JWT verifier must NOT be called for internal mux requests")
	assert.Equal(t, int64(1), reached.Load(), "handler must be reached")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestDualMux_PublicRoutes_StillEnforceJWT verifies that public-mux paths
// still go through JWT authentication: a /api/v1/* request without a token
// receives 401 from AuthMiddleware.
func TestDualMux_PublicRoutes_StillEnforceJWT(t *testing.T) {
	verifier := &dualMuxMockVerifier{err: errors.New("JWT enforced")}

	rtr, err := NewE(WithAuthMiddleware(verifier))
	require.NoError(t, err)

	rtr.Route("/api/v1/foo", func(sub kcell.RouteMux) {
		sub.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/foo/", nil)
	rec := httptest.NewRecorder()
	rtr.PublicHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"/api/v1/* without JWT must still return 401 from public mux AuthMiddleware")
}

// TestDualMux_WithInternalMiddleware_NilMiddleware_FailsFast ensures NewE
// rejects nil middleware entries to prevent silent mis-wiring.
func TestDualMux_WithInternalMiddleware_NilMiddleware_FailsFast(t *testing.T) {
	_, err := NewE(WithInternalMiddleware(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// TestDualMux_FinalizeAuth_RejectsDelegatedOnPublicPath verifies the
// consistency assertion: Delegated=true declared on a non-/internal/v1 path
// must fail FinalizeAuth.
func TestDualMux_FinalizeAuth_RejectsDelegatedOnPublicPath(t *testing.T) {
	rtr, err := NewE()
	require.NoError(t, err)

	// Simulate a Cell declaring Delegated:true on an /api/v1/* path.
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
// both pass. Routes are registered with a policy coverage whitelist rather
// than real handlers so the test focuses on the Delegated consistency rule.
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
