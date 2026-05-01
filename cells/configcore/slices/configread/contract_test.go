package configread

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

func TestHttpConfigGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.get.v1")

	// Lock the wire-level contract: drift in contract.yaml method/path would
	// silently break handlers registered via cells/configcore/cell.go.
	require.NotNil(t, c.HTTP, "http.config.get.v1 must declare endpoints.http")
	assert.Equal(t, "GET", c.HTTP.Method)
	assert.Equal(t, "/api/v1/config/{key}", c.HTTP.Path)

	// PR-CFG-C contract-as-auth-truth: route is admin-gated, so 403 must be a
	// first-class declared response, not just a runtime artifact.
	_, has403 := c.HTTP.Responses[403]
	assert.True(t, has403, "http.config.get.v1 must declare 403 (route is RoleAdmin-gated)")
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":{}}}`))

	// Non-sensitive entry: sensitive=false, value is the real value.
	c.ValidateResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp",`+
		`"sensitive":false,"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	// Sensitive entry: sensitive=true, value must be redacted.
	c.ValidateResponse(t, []byte(`{"data":{"id":"c-2","key":"db.password","value":"******",`+
		`"sensitive":true,"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
	// PR-A9: list-shape payloads belong to http.config.list.v1; the single-entry
	// contract must reject array data.
	c.MustRejectResponse(t, []byte(`{"data":[],"nextCursor":"","hasMore":false}`))
	// Missing sensitive field must be rejected (schema requires it).
	c.MustRejectResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp",`+
		`"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
}

func TestHttpConfigListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.list.v1")

	require.NotNil(t, c.HTTP, "http.config.list.v1 must declare endpoints.http")
	assert.Equal(t, "GET", c.HTTP.Method)
	assert.Equal(t, "/api/v1/config/", c.HTTP.Path)

	// PR-CFG-C contract-as-auth-truth: list endpoint is admin-gated.
	_, has403 := c.HTTP.Responses[403]
	assert.True(t, has403, "http.config.list.v1 must declare 403 (route is RoleAdmin-gated)")
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":{}}}`))

	// Non-sensitive entry: sensitive=false.
	c.ValidateResponse(t, []byte(`{"data":[{"id":"c-1","key":"app.name","value":"myapp",`+
		`"sensitive":false,"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],`+
		`"nextCursor":"","hasMore":false}`))
	// Sensitive entry: sensitive=true, value redacted.
	c.ValidateResponse(t, []byte(`{"data":[{"id":"c-2","key":"db.password","value":"******",`+
		`"sensitive":true,"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],`+
		`"nextCursor":"","hasMore":false}`))
	// Single-entry payload belongs to http.config.get.v1.
	c.MustRejectResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp",`+
		`"sensitive":false,"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	// Missing pagination envelope must be rejected.
	c.MustRejectResponse(t, []byte(`{"data":[]}`))
	// Missing sensitive field must be rejected (schema requires it for each item).
	c.MustRejectResponse(t, []byte(`{"data":[{"id":"c-1","key":"app.name","value":"myapp",`+
		`"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],`+
		`"nextCursor":"","hasMore":false}`))
}

// authzCase models a row in the runtime mux authz negative-test tables.
// principal=nil produces an anonymous request — the Mount's auth.AnyRole
// policy short-circuits to ErrAuthUnauthorized before role inspection.
// A non-nil principal exercises the authenticated-but-unauthorized branch
// (any role mismatch returns ErrAuthForbidden via principalHasAnyRole).
type authzCase struct {
	name        string
	principal   *auth.Principal
	wantStatus  int
	wantErrCode string
}

// setupInternalHandler wires the handler onto a celltest mux via
// RegisterInternalRoutes — nested under /internal/v1/config to mirror the
// production cell_routes.go InternalListener layout. The internal route
// applies auth.AnyRole(auth.RoleInternalAdmin); a request without a
// Principal must surface as 401 and a service principal without
// RoleInternalAdmin must surface as 403 — the same Policy production runs.
func setupInternalHandler() http.Handler {
	repo := mem.NewConfigRepository()
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	svc, err := NewService(repo, codec, slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	mux := celltest.NewTestMux()
	mux.Route("/internal/v1/config", func(sub kcell.RouteMux) {
		_ = NewHandler(svc).RegisterInternalRoutes(sub)
	})
	return mux
}

// runAuthzCases drives a table of authz cases through h with the given
// request. The single helper covers all three negative tests — primary
// list/get and internal get — by accepting an optional pre-built principal,
// keeping the assertion shape identical across listeners.
//
// In addition to status + error.code, the recorded body is validated
// against the contract's declared 4xx schemaRef so that drift in the
// shared error envelope (message / details / request_id presence) or in
// contract.yaml's responses[<status>] declaration is caught here, not
// only in upstream contract conformance tooling.
func runAuthzCases(t *testing.T, h http.Handler, c *contracttest.Contract, method, target string, cases []authzCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, target, nil)
			if tc.principal != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), tc.principal))
			}
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.wantStatus, rec.Code)
			body := rec.Body.Bytes()
			var resp struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(body, &resp))
			assert.Equal(t, tc.wantErrCode, resp.Error.Code)
			c.ValidateErrorResponse(t, tc.wantStatus, body)
		})
	}
}

// userPrincipal builds a PrincipalUser for table-driven authz cases. Mirrors
// auth.TestContext but exposes the *Principal so it can be embedded in the
// authzCase struct alongside service principals.
func userPrincipal(subject string, roles []string) *auth.Principal {
	return &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    subject,
		Roles:      append([]string(nil), roles...),
		AuthMethod: "test",
	}
}

// servicePrincipal builds a PrincipalService for the internal listener
// authz cases — paired with userPrincipal for symmetry.
func servicePrincipal(subject string, roles []string) *auth.Principal {
	return &auth.Principal{
		Kind:       auth.PrincipalService,
		Subject:    subject,
		Roles:      append([]string(nil), roles...),
		AuthMethod: "test",
	}
}

// TestHttpConfigGetV1_AuthzNegative locks the runtime auth-guard contract
// for the admin-gated GET /api/v1/config/{key}: anonymous requests are
// rejected with 401 ERR_AUTH_UNAUTHORIZED, and authenticated principals
// without the admin role are rejected with 403 ERR_AUTH_FORBIDDEN. The
// happy-path response shape is locked by TestHttpConfigGetV1Serve.
func TestHttpConfigGetV1_AuthzNegative(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.get.v1")
	h, _ := setupHandler()
	runAuthzCases(t, h, c, http.MethodGet, configBasePath+"/some-key", []authzCase{
		{"no_auth", nil, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", userPrincipal("user-1", []string{"viewer"}), http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
	})
}

// TestHttpConfigListV1_AuthzNegative locks the runtime auth-guard contract
// for the admin-gated GET /api/v1/config/ list endpoint, mirroring the
// single-entry GET semantics. The base path's trailing slash matches the
// production registration in cell_routes.go (mux.Route("/config", ...)).
func TestHttpConfigListV1_AuthzNegative(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.list.v1")
	h, _ := setupHandler()
	runAuthzCases(t, h, c, http.MethodGet, configBasePath+"/", []authzCase{
		{"no_auth", nil, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", userPrincipal("user-1", []string{"viewer"}), http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
	})
}

// TestHttpConfigInternalGetV1_PolicyDefenceInDepth locks the route-level
// Policy guard on /internal/v1/config/{key} as a defense-in-depth layer
// behind the listener's service-token authn chain, NOT the listener-level
// "missing or invalid service token" path declared by the contract — that
// path involves token parsing, nonce replay, and PrincipalService
// injection wired by composition root (cmd/corebundle.internalGuardFromEnv +
// runtime/auth.NewServiceTokenAuthenticator) and is exercised end-to-end
// in integration tests, not here. This test asserts:
//   - missing Principal → 401 (the route-policy short-circuit when the
//     listener auth chain failed to inject a Principal — guards against a
//     wiring regression that lets an unauth request reach the route)
//   - PrincipalService without RoleInternalAdmin → 403 (guards against a
//     downgrade of the route Policy to a weaker guard like AnyRole(any))
//
// The package-level TestMux (kernel/cell/celltest/mux.go) is intentionally
// scoped to route-policy semantics, so a real bundle.Run harness is the
// right fit for token-chain coverage; this slice-local test does not
// replace it.
func TestHttpConfigInternalGetV1_PolicyDefenceInDepth(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.internal.get.v1")
	h := setupInternalHandler()
	runAuthzCases(t, h, c, http.MethodGet, "/internal/v1/config/some-key", []authzCase{
		{"no_principal", nil, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"service_principal_without_role", servicePrincipal("svc-x", nil), http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
	})
}
