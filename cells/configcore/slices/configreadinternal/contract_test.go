package configreadinternal

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

// TestHttpConfigInternalGetV1_PathParamConstraints asserts that the key path
// param schema rejects empty string (violates minLength: 1).
func TestHttpConfigInternalGetV1_PathParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.internal.get.v1")
	c.ValidatePathParam(t, "key", "valid-key")
	c.MustRejectPathParam(t, "key", "") // violates minLength: 1
}

func TestHttpConfigInternalGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.internal.get.v1")

	require.NotNil(t, c.HTTP, "http.config.internal.get.v1 must declare endpoints.http")
	assert.Equal(t, "GET", c.HTTP.Method)
	assert.Equal(t, "/internal/v1/config/{key}", c.HTTP.Path)

	// Non-sensitive entry.
	c.ValidateResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp",`+
		`"sensitive":false,"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	// Sensitive entry: value redacted.
	c.ValidateResponse(t, []byte(`{"data":{"id":"c-2","key":"db.password","value":"******",`+
		`"sensitive":true,"version":1,`+
		`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

// NOTE: authzCase/runAuthzCases are intentionally duplicated with the sibling slice's
// contract_test.go — accepted architectural cost of the FMT-33 public/internal slice
// split (Go test-package isolation; cf. cell-patterns.md "跨 cell decode 重复属预期成本").
// Do not extract a shared testutil for 2 call sites.

// authzCase models a row in the runtime mux authz negative-test table.
type authzCase struct {
	name        string
	principal   *auth.Principal
	wantStatus  int
	wantErrCode string
}

// runAuthzCases drives a table of authz cases through h, validating status +
// error.code and the recorded body against the contract's declared 4xx
// schemaRef so drift in the shared error envelope or contract.yaml's
// responses[<status>] is caught here.
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

// servicePrincipal builds a PrincipalService for the internal listener authz
// cases (no caller-cell — fails the RequireCallerCell allowlist).
func servicePrincipal(subject string, roles []string) *auth.Principal {
	return &auth.Principal{
		Kind:       auth.PrincipalService,
		Subject:    subject,
		Roles:      append([]string(nil), roles...),
		AuthMethod: "test",
	}
}

// TestHttpConfigInternalGetV1_PolicyDefenceInDepth locks the route-level
// Policy guard on /internal/v1/config/{key} as a defense-in-depth layer
// behind the listener's service-token authn chain, NOT the listener-level
// "missing or invalid service token" path declared by the contract — that
// path involves token parsing, nonce replay, and PrincipalService injection
// wired by composition root and is exercised end-to-end in integration
// tests, not here. This test asserts:
//   - missing Principal → 401 (route-policy short-circuit when the listener
//     auth chain failed to inject a Principal — guards a wiring regression)
//   - PrincipalService not in the caller-cell allowlist → 403 (guards
//     against a downgrade of the route Policy to a weaker guard)
func TestHttpConfigInternalGetV1_PolicyDefenceInDepth(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.internal.get.v1")
	h, _ := setupHandler()
	runAuthzCases(t, h, c, http.MethodGet, internalBasePath+"/some-key", []authzCase{
		{"no_principal", nil, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"service_principal_not_allowlisted", servicePrincipal("svc-x", nil), http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
	})
}
