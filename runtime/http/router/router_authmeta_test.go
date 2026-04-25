package router

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Compile-time assertion
// ---------------------------------------------------------------------------

var _ kcell.AuthRouteDeclarer = (*Router)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// authMetaVerifier is an IntentTokenVerifier that returns configurable claims/err.
type authMetaVerifier struct {
	claims auth.Claims
	err    error
}

func (v *authMetaVerifier) Verify(_ context.Context, _ string) (auth.Claims, error) {
	return v.claims, v.err
}

func (v *authMetaVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	return v.claims, v.err
}

// okHandler writes 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// ---------------------------------------------------------------------------
// Nested Route adapter propagates declared metadata with composed prefix
// ---------------------------------------------------------------------------

func TestAuthDeclare_NestedRoute_ForwardsWithPrefix(t *testing.T) {
	r := New()

	// Cells commonly register routes under nested mux.Route scopes:
	//   mux.Route("/api/v1", func(v1) { v1.Route("/access", func(a) {
	//       a.Route("/sessions", func(s) { auth.Mount(s, Route{...}) })
	//   })})
	// The adapter chain must compose the mount prefixes so the declared
	// meta reaches the Router with the full path.
	r.Route("/api/v1", func(v1 kcell.RouteMux) {
		v1.Route("/access", func(a kcell.RouteMux) {
			a.Route("/sessions", func(s kcell.RouteMux) {
				auth.Mount(s, auth.Route{Contract: testHTTPContract("POST", "/login"), Handler: okHandler, Public: true})
				auth.Mount(s, auth.Route{Contract: testHTTPContract("DELETE", "/{id}"), Handler: okHandler, Policy: auth.Authenticated(), PasswordResetExempt: true})
			})
		})
	})

	require.Len(t, r.declaredAuthMetas, 2)
	assert.Equal(t, "/api/v1/access/sessions/login", r.declaredAuthMetas[0].Path)
	assert.True(t, r.declaredAuthMetas[0].Public)
	assert.Equal(t, "/api/v1/access/sessions/{id}", r.declaredAuthMetas[1].Path)
	assert.True(t, r.declaredAuthMetas[1].PasswordResetExempt)
}

// ---------------------------------------------------------------------------
// DeclareAuthMeta accumulates metas
// ---------------------------------------------------------------------------

func TestDeclareAuthMeta_Accumulates(t *testing.T) {
	r := New()
	m1 := kcell.AuthRouteMeta{Method: "GET", Path: "/a", Public: true}
	m2 := kcell.AuthRouteMeta{Method: "POST", Path: "/b", PasswordResetExempt: true}

	r.DeclareAuthMeta(m1)
	r.DeclareAuthMeta(m2)

	require.Len(t, r.declaredAuthMetas, 2)
	assert.Equal(t, m1, r.declaredAuthMetas[0])
	assert.Equal(t, m2, r.declaredAuthMetas[1])
}

// ---------------------------------------------------------------------------
// FinalizeAuth: empty declaration is a no-op; authFinalized becomes true
// ---------------------------------------------------------------------------

func TestFinalizeAuth_EmptyDeclaration_NoOp(t *testing.T) {
	r := New()
	err := r.FinalizeAuth()
	require.NoError(t, err)
	assert.True(t, r.authFinalized)
}

// ---------------------------------------------------------------------------
// R2-11: WithSuppressNoAuthVerifierWarn silences the FinalizeAuth Warn that
// fires when auth metas are declared on a router with no AuthMiddleware.
// HealthListener and InternalListener routers must use this opt-out.
// ---------------------------------------------------------------------------

func captureSlogWarn(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return &buf, func() { slog.SetDefault(prev) }
}

func TestFinalizeAuth_NoVerifier_EmitsWarn_ByDefault(t *testing.T) {
	buf, restore := captureSlogWarn(t)
	defer restore()

	r := New() // no AuthMiddleware, no suppression
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/healthz"), Handler: okHandler, Public: true})
	require.NoError(t, r.FinalizeAuth())

	assert.Contains(t, buf.String(), "AuthMiddleware is not installed",
		"FinalizeAuth must warn when authVerifier is nil and metas are declared (R2-11 baseline)")
}

func TestFinalizeAuth_NoVerifier_SuppressedWarn_NoOutput(t *testing.T) {
	buf, restore := captureSlogWarn(t)
	defer restore()

	// Mirrors how bootstrap wires HealthListener routers post-R2-11.
	r, err := NewE(WithSuppressNoAuthVerifierWarn())
	require.NoError(t, err)
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/healthz"), Handler: okHandler, Public: true})
	require.NoError(t, r.FinalizeAuth())

	assert.NotContains(t, buf.String(), "AuthMiddleware is not installed",
		"WithSuppressNoAuthVerifierWarn must silence the no-verifier Warn at FinalizeAuth (R2-11)")
}

// ---------------------------------------------------------------------------
// FinalizeAuth compiles Public metas into authPublicMatcher
// ---------------------------------------------------------------------------

func TestFinalizeAuth_PublicMeta_BypassesAuth(t *testing.T) {
	verifier := &authMetaVerifier{err: assert.AnError} // should not be called for public
	r, err := NewE(WithAuthMiddleware(verifier))
	require.NoError(t, err)

	// Use auth.Mount so every registered route has a corresponding auth declaration.
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/public"), Handler: okHandler, Public: true})
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/protected"), Handler: okHandler, Policy: auth.Authenticated()})
	require.NoError(t, r.FinalizeAuth())

	// Public route: no token → 200
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "public route must bypass auth")

	// Protected route: no token → 401
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "non-public route must require auth")
}

// ---------------------------------------------------------------------------
// FinalizeAuth compiles PasswordResetExempt metas
// ---------------------------------------------------------------------------

func TestFinalizeAuth_PasswordResetExempt_Meta(t *testing.T) {
	verifier := &authMetaVerifier{
		claims: auth.Claims{Subject: "usr-1", PasswordResetRequired: true},
	}
	r, err := NewE(WithAuthMiddleware(verifier))
	require.NoError(t, err)

	// Use auth.Mount so every registered route has a corresponding auth declaration.
	auth.Mount(r, auth.Route{Contract: testHTTPContract("POST", "/exempt"), Handler: okHandler, PasswordResetExempt: true})
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/blocked"), Handler: okHandler, Policy: auth.Authenticated()})
	require.NoError(t, r.FinalizeAuth())

	// Exempt route with password-reset token → 200
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/exempt", nil)
	req.Header.Set("Authorization", "Bearer any-token")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "exempt route must pass through password-reset gate")

	// Non-exempt route with password-reset token → 403
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/blocked", nil)
	req.Header.Set("Authorization", "Bearer any-token")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code, "non-exempt route must be blocked by password-reset gate")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_AUTH_PASSWORD_RESET_REQUIRED", errObj["code"])
}

// ---------------------------------------------------------------------------
// Duplicate (method, path) → FinalizeAuth returns error
// ---------------------------------------------------------------------------

func TestFinalizeAuth_DuplicateMeta_ReturnsError(t *testing.T) {
	r := New()
	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/dup", Public: true})
	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/dup", Public: true})

	err := r.FinalizeAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate auth declaration")
}

// ---------------------------------------------------------------------------
// DeclareAuthMeta after FinalizeAuth panics
// ---------------------------------------------------------------------------

func TestDeclareAuthMeta_AfterFinalized_Panics(t *testing.T) {
	r := New()
	require.NoError(t, r.FinalizeAuth())

	assert.Panics(t, func() {
		r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/late", Public: true})
	}, "DeclareAuthMeta after FinalizeAuth must panic")
}

// ---------------------------------------------------------------------------
// FinalizeAuth called twice → second call returns error
// ---------------------------------------------------------------------------

func TestFinalizeAuth_CalledTwice_ReturnsError(t *testing.T) {
	r := New()
	require.NoError(t, r.FinalizeAuth())

	err := r.FinalizeAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FinalizeAuth called twice")
}

// ---------------------------------------------------------------------------
// Hint derivation from declared POST + PasswordResetExempt meta
// ---------------------------------------------------------------------------

func TestFinalizeAuth_HintDerivedFromPostExemptMeta(t *testing.T) {
	verifier := &authMetaVerifier{
		claims: auth.Claims{Subject: "usr-1", PasswordResetRequired: true},
	}
	r, err := NewE(WithAuthMiddleware(verifier))
	require.NoError(t, err)

	// Use auth.Mount so every registered route has a corresponding auth declaration.
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/blocked"), Handler: okHandler, Policy: auth.Authenticated()})
	// POST + PasswordResetExempt meta should derive hint.
	auth.Mount(r, auth.Route{Contract: testHTTPContract("POST", "/change-password"), Handler: okHandler, PasswordResetExempt: true})
	require.NoError(t, r.FinalizeAuth())

	// Non-exempt route → 403 with change_password_endpoint hint
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/blocked", nil)
	req.Header.Set("Authorization", "Bearer any-token")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	details, ok := errObj["details"].(map[string]any)
	require.True(t, ok, "details must be present when hint is derived")
	assert.Equal(t, "POST /change-password", details["change_password_endpoint"])
}

// ---------------------------------------------------------------------------
// Multiple declared Public metas are OR-merged by FinalizeAuth
// ---------------------------------------------------------------------------

func TestFinalizeAuth_MultipleDeclaredPublic_ORMerged(t *testing.T) {
	// Both declared-public-a and declared-public-b should bypass auth;
	// /protected must still require a token.
	verifier := &authMetaVerifier{err: assert.AnError}
	r, err := NewE(WithAuthMiddleware(verifier))
	require.NoError(t, err)

	// Use auth.Mount so every registered route has a corresponding auth declaration.
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/declared-public-a"), Handler: okHandler, Public: true})
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/declared-public-b"), Handler: okHandler, Public: true})
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/protected"), Handler: okHandler, Policy: auth.Authenticated()})
	require.NoError(t, r.FinalizeAuth())

	// First declared public route: no token → 200
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/declared-public-a", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "first declared public endpoint must bypass auth")

	// Second declared public route: no token → 200
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/declared-public-b", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "second declared public endpoint must bypass auth")

	// Protected route: no token → 401
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "unrelated route must still require auth")
}

// ---------------------------------------------------------------------------
// F3: ServeHTTP panics when metas declared but FinalizeAuth not called
// ---------------------------------------------------------------------------

func TestServeHTTP_AuthMetasWithoutFinalize_Panics(t *testing.T) {
	r := New()
	r.Handle("/guarded", okHandler)
	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/guarded", Public: true})
	// FinalizeAuth intentionally NOT called.

	assert.PanicsWithValue(t,
		"router: FinalizeAuth must be called before ServeHTTP when auth route metadata has been declared",
		func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
			r.ServeHTTP(rec, req)
		},
		"ServeHTTP must panic when metas are declared but FinalizeAuth was not called",
	)
}

func TestServeHTTP_NoMetas_NoFinalize_OK(t *testing.T) {
	r := New()
	r.Handle("/hello", okHandler)
	// No auth.Mount calls, no FinalizeAuth — should work fine.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	assert.NotPanics(t, func() {
		r.ServeHTTP(rec, req)
	}, "Router with no declarations must not require FinalizeAuth")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// ---------------------------------------------------------------------------
// F4: FinalizeAuth logs a warning when metas declared but no verifier
// ---------------------------------------------------------------------------

func TestFinalizeAuth_NoVerifier_LogsWarning(t *testing.T) {
	// Capture slog output via a JSON handler.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	prev := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(prev)

	r := New() // no WithAuthMiddleware
	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/public-route", Public: true})
	require.NoError(t, r.FinalizeAuth())

	logged := buf.String()
	assert.Contains(t, logged, "AuthMiddleware is not installed", "expected warning about missing AuthMiddleware")
	assert.Contains(t, logged, "WARN", "expected WARN level")
}

// ---------------------------------------------------------------------------
// PR-A14a note: the former TestFinalizeAuth_DelegatedMeta_BypassesJWT,
// TestFinalizeAuth_DelegatedMeta_ORMergesWithInternalGuard, and
// TestFinalizeAuth_MethodCaseNormalisation tests were removed. They exercised
// the delegated-matcher bypass inside the public-mux JWT middleware, which no
// longer exists: /internal/v1/* routes live on a physically separate mux
// (internalMux) served by InternalHandler(), and JWT middleware runs only on
// publicMux. See router_dual_mux_test.go:
//   - TestDualMux_InternalRoutes_NeverReachJWTMiddleware
//   - TestDualMux_InternalMiddleware_AppliedToInternalMuxOnly
//   - TestDualMux_PublicRoutes_StillEnforceJWT
//   - TestDualMux_FinalizeAuth_Rejects{Delegated,NonDelegated}* (consistency)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Policy coverage verification tests
// ---------------------------------------------------------------------------

func TestFinalizeAuth_PolicyCoverage_DetectsMissingPolicy(t *testing.T) {
	// A route registered via raw Handle without auth.Mount must cause
	// FinalizeAuth to return an error listing the uncovered route.
	r, err := NewE(WithAuthMiddleware(&authMetaVerifier{err: assert.AnError}))
	require.NoError(t, err)

	// /unguarded is registered without auth.Mount — coverage violation.
	r.Handle("GET /unguarded", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	// /guarded is registered via auth.Mount — covered.
	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/guarded"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}), Policy: auth.Authenticated()})

	err = r.FinalizeAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GET /unguarded", "error must list the uncovered route")
	assert.NotContains(t, err.Error(), "GET /guarded", "covered route must not appear in error")
}

func TestFinalizeAuth_PolicyCoverage_AllDeclaredOK(t *testing.T) {
	// All registered routes have auth.Mount — FinalizeAuth must succeed.
	r, err := NewE(WithAuthMiddleware(&authMetaVerifier{err: assert.AnError}))
	require.NoError(t, err)

	auth.Mount(r, auth.Route{Contract: testHTTPContract("GET", "/api/v1/items"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}), Policy: auth.Authenticated()})
	auth.Mount(r, auth.Route{Contract: testHTTPContract("POST", "/api/v1/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}), Public: true})

	err = r.FinalizeAuth()
	require.NoError(t, err)
}

func TestFinalizeAuth_PolicyCoverage_WhitelistExempts(t *testing.T) {
	// Routes matching WithPolicyCoverageWhitelist patterns are exempt from
	// coverage enforcement even when registered via raw Handle.
	r, err := NewE(
		WithPolicyCoverageWhitelist([]string{"/debug/*"}),
		WithAuthMiddleware(&authMetaVerifier{err: assert.AnError}),
	)
	require.NoError(t, err)

	// Registered without auth.Mount but whitelisted via prefix pattern.
	r.Handle("GET /debug/pprof", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	err = r.FinalizeAuth()
	require.NoError(t, err, "whitelisted route must not trigger policy coverage error")
}
