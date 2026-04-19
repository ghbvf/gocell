package router

import (
	"context"
	"encoding/json"
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
	r, err := NewE(WithAuthMiddleware(verifier, nil))
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
	r, err := NewE(WithAuthMiddleware(verifier, nil))
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
	r, err := NewE(WithAuthMiddleware(verifier, nil))
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
// Coexistence: legacy WithPublicEndpoints + declared Public metas (OR-merged)
// ---------------------------------------------------------------------------

func TestFinalizeAuth_Coexistence_LegacyAndDeclared(t *testing.T) {
	verifier := &authMetaVerifier{err: assert.AnError}
	r, err := NewE(
		WithPublicEndpoints([]string{"GET /legacy-public"}),
		WithAuthMiddleware(verifier, nil),
	)
	require.NoError(t, err)

	r.Handle("/legacy-public", okHandler)
	r.Handle("/declared-public", okHandler)
	r.Handle("/protected", okHandler)

	r.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/declared-public", Public: true})
	require.NoError(t, r.FinalizeAuth())

	// Legacy public route: no token → 200
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/legacy-public", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "legacy public endpoint must still bypass auth")

	// Declared public route: no token → 200
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/declared-public", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "declared public endpoint must bypass auth")

	// Protected route: no token → 401
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "unrelated route must still require auth")
}
