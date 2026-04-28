package configread

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHttpConfigGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.get.v1")

	// Lock the wire-level contract: drift in contract.yaml method/path would
	// silently break handlers registered via cells/configcore/cell.go.
	require.NotNil(t, c.HTTP, "http.config.get.v1 must declare endpoints.http")
	assert.Equal(t, "GET", c.HTTP.Method)
	assert.Equal(t, "/api/v1/config/{key}", c.HTTP.Path)

	// PR-CFG-C contract-as-auth-truth: route is admin-gated, so 403 must be a
	// first-class declared response, not just a runtime artefact.
	_, has403 := c.HTTP.Responses[403]
	assert.True(t, has403, "http.config.get.v1 must declare 403 (route is RoleAdmin-gated)")
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":{}}}`))

	// Non-sensitive entry: sensitive=false, value is the real value.
	c.ValidateResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp","sensitive":false,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	// Sensitive entry: sensitive=true, value must be redacted.
	c.ValidateResponse(t, []byte(`{"data":{"id":"c-2","key":"db.password","value":"******","sensitive":true,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
	// PR-A9: list-shape payloads belong to http.config.list.v1; the single-entry
	// contract must reject array data.
	c.MustRejectResponse(t, []byte(`{"data":[],"nextCursor":"","hasMore":false}`))
	// Missing sensitive field must be rejected (schema requires it).
	c.MustRejectResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp","version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
}

func TestHttpConfigListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.list.v1")

	require.NotNil(t, c.HTTP, "http.config.list.v1 must declare endpoints.http")
	assert.Equal(t, "GET", c.HTTP.Method)
	assert.Equal(t, "/api/v1/config/", c.HTTP.Path)

	// PR-CFG-C contract-as-auth-truth: list endpoint is admin-gated.
	_, has403 := c.HTTP.Responses[403]
	assert.True(t, has403, "http.config.list.v1 must declare 403 (route is RoleAdmin-gated)")
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":{}}}`))

	// Non-sensitive entry: sensitive=false.
	c.ValidateResponse(t, []byte(`{"data":[{"id":"c-1","key":"app.name","value":"myapp","sensitive":false,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],"nextCursor":"","hasMore":false}`))
	// Sensitive entry: sensitive=true, value redacted.
	c.ValidateResponse(t, []byte(`{"data":[{"id":"c-2","key":"db.password","value":"******","sensitive":true,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],"nextCursor":"","hasMore":false}`))
	// Single-entry payload belongs to http.config.get.v1.
	c.MustRejectResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp","sensitive":false,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	// Missing pagination envelope must be rejected.
	c.MustRejectResponse(t, []byte(`{"data":[]}`))
	// Missing sensitive field must be rejected (schema requires it for each item).
	c.MustRejectResponse(t, []byte(`{"data":[{"id":"c-1","key":"app.name","value":"myapp","version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],"nextCursor":"","hasMore":false}`))
}

// authzCase models a row in the authz negative-test tables. injectAuth=false
// produces an anonymous request (no Principal in context) — the Mount's
// auth.AnyRole policy short-circuits to ErrAuthUnauthorized before role
// inspection. injectAuth=true with a non-admin role exercises the
// "authenticated-but-unauthorized" branch and produces ErrAuthForbidden.
type authzCase struct {
	name        string
	injectAuth  bool
	subject     string
	roles       []string
	wantStatus  int
	wantErrCode string
}

// runAuthzCases drives a table of authz cases through h with the given
// request — extracted to avoid duplicating the same recorder/decode/assert
// dance across the three negative tests.
func runAuthzCases(t *testing.T, h http.Handler, method, target string, cases []authzCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, target, nil)
			if tc.injectAuth {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.wantStatus, rec.Code)
			var resp struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tc.wantErrCode, resp.Error.Code)
		})
	}
}

// setupInternalHandler wires the handler onto a celltest mux via
// RegisterInternalRoutes — nested under /internal/v1/config to mirror the
// production cell_routes.go InternalListener layout. The internal route
// applies auth.AnyRole(auth.RoleInternalAdmin); a request without a
// Principal must surface as 401 (listener service-token chain absent in
// this in-process test) and a service principal without RoleInternalAdmin
// must surface as 403 — the same Policy that production runs.
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

// TestHttpConfigGetV1_AuthzNegative locks the runtime auth-guard contract
// for the admin-gated GET /api/v1/config/{key}: anonymous requests are
// rejected with 401 ERR_AUTH_UNAUTHORIZED, and authenticated principals
// without the admin role are rejected with 403 ERR_AUTH_FORBIDDEN. The
// happy-path response shape is locked separately by TestHttpConfigGetV1Serve
// via contract schema validation.
func TestHttpConfigGetV1_AuthzNegative(t *testing.T) {
	h, _ := setupHandler()
	runAuthzCases(t, h, http.MethodGet, configBasePath+"/some-key", []authzCase{
		{"no_auth", false, "", nil, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", true, "user-1", []string{"viewer"}, http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
	})
}

// TestHttpConfigListV1_AuthzNegative locks the runtime auth-guard contract
// for the admin-gated GET /api/v1/config/ list endpoint, mirroring the
// single-entry GET semantics. The base path's trailing slash matches the
// production registration in cell_routes.go (mux.Route("/config", ...)).
func TestHttpConfigListV1_AuthzNegative(t *testing.T) {
	h, _ := setupHandler()
	runAuthzCases(t, h, http.MethodGet, configBasePath+"/", []authzCase{
		{"no_auth", false, "", nil, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", true, "user-1", []string{"viewer"}, http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
	})
}

// TestHttpConfigInternalGetV1_AuthzNegative locks the auth-guard contract
// for the internal-listener GET /internal/v1/config/{key}: a missing
// principal produces 401 (defence-in-depth alongside the listener's
// service-token chain), and a service principal lacking RoleInternalAdmin
// produces 403. The service-principal pattern matches runtime/auth/authz_test.go's
// PrincipalService cases (cell handlers see Principal regardless of source).
func TestHttpConfigInternalGetV1_AuthzNegative(t *testing.T) {
	h := setupInternalHandler()
	target := "/internal/v1/config/some-key"

	// Anonymous: no Principal in context.
	t.Run("no_principal", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		var resp struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", resp.Error.Code)
	})

	// Service principal without RoleInternalAdmin: authenticated but unauthorized.
	t.Run("service_principal_without_role", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		ctx := auth.WithPrincipal(req.Context(), &auth.Principal{
			Kind:       auth.PrincipalService,
			Subject:    "svc-x",
			Roles:      []string{},
			AuthMethod: "test",
		})
		h.ServeHTTP(rec, req.WithContext(ctx))
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "ERR_AUTH_FORBIDDEN", resp.Error.Code)
	})
}
