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
	//       a.Route("/sessions", func(s) { auth.Declare(s, RouteDecl{...}) })
	//   })})
	// The adapter chain must compose the mount prefixes so the declared
	// meta reaches the Router with the full path.
	r.Route("/api/v1", func(v1 kcell.RouteMux) {
		v1.Route("/access", func(a kcell.RouteMux) {
			a.Route("/sessions", func(s kcell.RouteMux) {
				auth.Declare(s, auth.RouteDecl{
					Method:  "POST",
					Path:    "/login",
					Handler: okHandler,
					Public:  true,
				})
				auth.Declare(s, auth.RouteDecl{
					Method:              "DELETE",
					Path:                "/{id}",
					Handler:             okHandler,
					Policy:              auth.Authenticated(),
					PasswordResetExempt: true,
				})
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
// FinalizeAuth compiles Public metas into authPublicMatcher
// ---------------------------------------------------------------------------

func TestFinalizeAuth_PublicMeta_BypassesAuth(t *testing.T) {
	verifier := &authMetaVerifier{err: assert.AnError} // should not be called for public
	r, err := NewE(WithAuthMiddleware(verifier))
	require.NoError(t, err)

	r.Handle("/public", okHandler)
	r.Handle("/protected", okHandler)

	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/public", Public: true})
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

	r.Handle("/exempt", okHandler)
	r.Handle("/blocked", okHandler)

	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "POST", Path: "/exempt", PasswordResetExempt: true})
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

	r.Handle("/blocked", okHandler)
	r.Handle("/change-password", okHandler)

	// No legacy hint set; POST + PasswordResetExempt meta should derive hint
	r.DeclareAuthMeta(kcell.AuthRouteMeta{
		Method:              "POST",
		Path:                "/change-password",
		PasswordResetExempt: true,
	})
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
	assert.Equal(t, "/change-password", details["change_password_endpoint"])
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

	r.Handle("/declared-public-a", okHandler)
	r.Handle("/declared-public-b", okHandler)
	r.Handle("/protected", okHandler)

	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/declared-public-a", Public: true})
	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/declared-public-b", Public: true})
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
	// No auth.Declare calls, no FinalizeAuth — should work fine.

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
// F7-1: Delegated meta bypasses JWT, non-delegated meta requires JWT
// ---------------------------------------------------------------------------

func TestFinalizeAuth_DelegatedMeta_BypassesJWT(t *testing.T) {
	// verifier always returns an error — so any JWT verification would yield 401.
	verifier := &authMetaVerifier{err: assert.AnError}
	r, err := NewE(WithAuthMiddleware(verifier))
	require.NoError(t, err)

	r.Handle("/delegated", okHandler)
	r.Handle("/normal", okHandler)

	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/delegated", Delegated: true})
	// /normal has no meta → it requires JWT.
	require.NoError(t, r.FinalizeAuth())

	// Delegated route: no token → 200 (JWT verification skipped).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/delegated", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "delegated route must bypass JWT verification")

	// Non-delegated route: no token → 401.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/normal", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "non-delegated route must require JWT")
}

// ---------------------------------------------------------------------------
// F7-2: OR-merge of internal prefix guard + declared Delegated meta
// ---------------------------------------------------------------------------

func TestFinalizeAuth_DelegatedMeta_ORMergesWithInternalGuard(t *testing.T) {
	// Internal guard blocks with 403 so we can distinguish it from 401 (JWT failure).
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simplified guard: accept requests with X-Service-Token header.
			if r.Header.Get("X-Service-Token") == "secret" {
				next.ServeHTTP(w, r)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		})
	}
	verifier := &authMetaVerifier{err: assert.AnError}
	r, err := NewE(
		WithAuthMiddleware(verifier),
		WithInternalPathPrefixGuard("/internal/v1/", guard),
	)
	require.NoError(t, err)

	// Declare a delegated route outside the internal prefix.
	r.Handle("/api/v1/svc-route", okHandler)
	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/api/v1/svc-route", Delegated: true})
	require.NoError(t, r.FinalizeAuth())

	// Route under /internal/v1/ is already delegated via the guard option.
	r.Handle("/internal/v1/thing", okHandler)

	// Internal prefix route: JWT skipped (delegated), guard passes with token.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/thing", nil)
	req.Header.Set("X-Service-Token", "secret")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "internal prefix route must reach guard")

	// Declared delegated route outside internal prefix: JWT skipped, 200.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/svc-route", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "declared delegated route must bypass JWT")
}

// ---------------------------------------------------------------------------
// F7-3: Method case normalisation — declared uppercase METHOD matches requests
// ---------------------------------------------------------------------------

func TestFinalizeAuth_MethodCaseNormalisation(t *testing.T) {
	// Verifier errors so any JWT check → 401; a successful response means auth was skipped.
	verifier := &authMetaVerifier{err: assert.AnError}
	r, err := NewE(WithAuthMiddleware(verifier))
	require.NoError(t, err)

	r.Handle("/submit", okHandler)

	// Method declared as "POST" (uppercase — validateOrPanic enforces this).
	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "POST", Path: "/submit", Delegated: true})
	require.NoError(t, r.FinalizeAuth())

	// net/http canonicalises Method to uppercase for incoming requests, so POST
	// from a real client always arrives as "POST". The compiled matcher uses
	// strings.EqualFold so it is case-tolerant; verify that standard "POST"
	// matches as expected.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "POST declared delegated route must bypass JWT verification")
}
