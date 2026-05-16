package configread

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
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
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":[]}}`))

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
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":[]}}`))

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

// NOTE: authzCase/runAuthzCases are intentionally duplicated with the sibling slice's
// contract_test.go — accepted architectural cost of the FMT-33 public/internal slice
// split (Go test-package isolation; cf. cell-patterns.md "跨 cell decode 重复属预期成本").
// Do not extract a shared testutil for 2 call sites.

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

// runAuthzCases drives a table of authz cases through h with the given
// request. In addition to status + error.code, the recorded body is
// validated against the contract's declared 4xx schemaRef so that drift in
// the shared error envelope or in contract.yaml's responses[<status>]
// declaration is caught here, not only in upstream contract conformance.
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
// authzCase struct.
func userPrincipal(subject string, roles []string) *auth.Principal {
	return &auth.Principal{
		Kind:       auth.PrincipalUser,
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
// production registration in cell_gen.go (mux.Route("/config", ...)).
func TestHttpConfigListV1_AuthzNegative(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.list.v1")
	h, _ := setupHandler()
	runAuthzCases(t, h, c, http.MethodGet, configBasePath+"/", []authzCase{
		{"no_auth", nil, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", userPrincipal("user-1", []string{"viewer"}), http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
	})
}
