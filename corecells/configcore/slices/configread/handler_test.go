package configread

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/configcore/configcoretest"
	"github.com/ghbvf/gocell/corecells/configcore/internal/configreader"
	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/corecells/configcore/internal/dto"
	"github.com/ghbvf/gocell/corecells/configcore/internal/mem"
	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	configget "github.com/ghbvf/gocell/generated/contracts/http/config/get/v1"
)

// allowAuthorizer / withAllowAuthorizer / withDenyAuthorizer build the PDP
// verdicts for tests. The authz.Allow/Deny construction lives in _test.go,
// which AUTHZ-DECISION-ALLOW-DENY-CALLER-01 sanctions for test doubles; the
// CapturingAuthorizer type and WithAuthorizer ctx wiring are shared via
// configcoretest (PR-10b #1348).
func allowAuthorizer() *configcoretest.CapturingAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("test allowAuthorizer: authz.Allow: " + err.Error())
	}
	return &configcoretest.CapturingAuthorizer{Decision: dec}
}

func withAllowAuthorizer(ctx context.Context) context.Context {
	return configcoretest.WithAuthorizer(ctx, allowAuthorizer())
}

func withDenyAuthorizer(ctx context.Context, reason string) context.Context {
	return configcoretest.WithAuthorizer(ctx, &configcoretest.CapturingAuthorizer{Decision: authz.Deny(reason)})
}

// testReadTenant is the typed TenantID for direct repo seeding.
var testReadTenant = configcoretest.TestTenant

const configBasePath = "/api/v1/config"

// asAdmin attaches an admin Principal, a valid TenantID, and an allow Authorizer
// to req so it satisfies the contract-derived PDP gate (#2205, auth.RequirePermissionForContract)
// AND the configread handler's tenant.FromContext call.
func asAdmin(req *http.Request) *http.Request {
	ctx := withAllowAuthorizer(
		configcoretest.CtxWithTenant(auth.TestContext("admin-user", []string{auth.RoleAdmin})),
	)
	return req.WithContext(ctx)
}

// testConfigReadResolver is the contract-derived resolver for configread handler tests,
// mirroring the cellHTTPResolver built by cellgen from endpoints.http.permission overlays.
var testConfigReadResolver = auth.NewStaticMethodPolicyResolver(map[string]string{
	"http.config.get.v1":  "config:read",
	"http.config.list.v1": "config:read",
})

// setupHandler wires the slice handler onto a celltest mux via RegisterRoutes —
// nested under /api/v1/config to match the production cell_routes.go layout.
func setupHandler() (http.Handler, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository(clock.Real())
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	svc, err := configreader.NewService(repo, outbox.DemoCellTxManager(), codec, slog.Default(), "configread", query.RunModeProd)
	if err != nil {
		panic(err)
	}
	mux := celltest.NewTestMux()
	mux.Route(configBasePath, func(sub kcell.RouteMux) {
		if err := NewHandler(svc, testConfigReadResolver).RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
	return mux, repo
}

func TestHandler_HandleGet_Found(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/app.name", nil)
	handler.ServeHTTP(w, asAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "gocell")

	// Verify camelCase JSON keys (#27n).
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	var dataMap map[string]any
	require.NoError(t, json.Unmarshal(raw["data"], &dataMap))
	assert.Contains(t, dataMap, "id", "key must be camelCase")
	assert.Contains(t, dataMap, "key", "key must be camelCase")
	assert.Contains(t, dataMap, "value", "key must be camelCase")
	assert.Contains(t, dataMap, "sensitive", "key must be camelCase")
	assert.Contains(t, dataMap, "version", "key must be camelCase")
	assert.Contains(t, dataMap, "createdAt", "key must be camelCase")
	assert.Contains(t, dataMap, "updatedAt", "key must be camelCase")
}

func TestHandler_HandleGet_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/missing-key", nil)
	handler.ServeHTTP(w, asAdmin(req))

	errcodetest.AssertWireCode(t, w, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
}

// asAdminNoTenant attaches an admin Principal and an allow Authorizer but NO
// TenantID, so the request passes the PDP gate yet fails tenant.FromContext.
// F6: this must map to a typed 403, not a framework 500.
func asAdminNoTenant(req *http.Request) *http.Request {
	ctx := withAllowAuthorizer(auth.TestContext("admin-user", []string{auth.RoleAdmin}))
	return req.WithContext(ctx)
}

func TestHandler_HandleGet_MissingTenant_403(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/app.name", nil)
	handler.ServeHTTP(w, asAdminNoTenant(req))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_HandleList_MissingTenant_403(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/?limit=1", nil)
	handler.ServeHTTP(w, asAdminNoTenant(req))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_HandleList_OK(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/", nil)
	handler.ServeHTTP(w, asAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"data\":")
	assert.Contains(t, w.Body.String(), "\"hasMore\":")
}

func TestHandler_HandleList_Empty(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/", nil)
	handler.ServeHTTP(w, asAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"data\":")
	assert.Contains(t, w.Body.String(), "\"hasMore\":false")
}

func TestHandler_HandleList_InvalidLimit(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/?limit=abc", nil)
	handler.ServeHTTP(w, asAdmin(req))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandler_HandleList_ExceedsMaxLimit(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/?limit=501", nil)
	handler.ServeHTTP(w, asAdmin(req))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandler_HandleList_Pagination_FullTraversal(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	keys := []string{"key-a", "key-b", "key-c", "key-d", "key-e", "key-f", "key-g"}
	for i, k := range keys {
		require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
			ID: "cfg-" + k, Key: k, Value: "v" + k, Version: 1,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}))
	}

	var allIDs []string
	cursor := ""

	for range 10 {
		url := configBasePath + "/?limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		handler.ServeHTTP(w, asAdmin(req))

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		data := resp["data"].([]any)
		for _, item := range data {
			m := item.(map[string]any)
			id, ok := m["id"].(string)
			require.True(t, ok, "response item should have string 'id' field")
			allIDs = append(allIDs, id)
		}

		hasMore := resp["hasMore"].(bool)
		if !hasMore {
			break
		}
		cursor = resp["nextCursor"].(string)
		require.NotEmpty(t, cursor)
	}

	// All 7 items collected, no duplicates
	assert.Len(t, allIDs, 7)
	seen := make(map[string]bool)
	for _, id := range allIDs {
		assert.False(t, seen[id], "duplicate ID: %s", id)
		seen[id] = true
	}
}

func TestHandler_HandleList_InvalidCursor(t *testing.T) {
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))

	wrongSort := []query.SortColumn{{Name: "other", Direction: query.SortASC}, {Name: "x", Direction: query.SortASC}}
	missingFieldsToken, _ := codec.Encode(query.Cursor{Values: []any{"v1", "v2"}})
	crossContextToken, _ := codec.Encode(query.Cursor{
		Values:  []any{"v1", "v2"},
		Scope:   query.SortScope(wrongSort),
		Context: query.QueryContext("endpoint", "wrong-endpoint"),
	})

	tests := []struct {
		name   string
		cursor string
	}{
		{"garbage token", "not-a-valid-cursor!!!"},
		{"missing scope and context", missingFieldsToken},
		{"cross-context replay", crossContextToken},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := setupHandler()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, configBasePath+"/?cursor="+tc.cursor, nil)
			handler.ServeHTTP(w, asAdmin(req))

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "ERR_CURSOR_INVALID")
		})
	}
}

// Sensitive value redaction tests (#27o).
func TestHandler_HandleGet_SensitiveRedacted(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
		ID: "cfg-s1", Key: "db.password", Value: "s3cret!", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/db.password", nil)
	handler.ServeHTTP(w, asAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data configget.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, dto.RedactedValue, resp.Data.Value)
	assert.True(t, resp.Data.Sensitive)
	assert.NotContains(t, w.Body.String(), "s3cret!")
}

func TestHandler_HandleGet_NonSensitiveVisible(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
		ID: "cfg-n1", Key: "app.name", Value: "gocell", Sensitive: false,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/app.name", nil)
	handler.ServeHTTP(w, asAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data configget.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "gocell", resp.Data.Value)
	assert.False(t, resp.Data.Sensitive)
}

func TestHandler_HandleList_SensitiveRedacted(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Sensitive: false,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
		ID: "cfg-2", Key: "api.key", Value: "sk-secret-key-123", Sensitive: true,
		Version: 1, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/", nil)
	handler.ServeHTTP(w, asAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "gocell")
	assert.NotContains(t, body, "sk-secret-key-123")
	assert.Contains(t, body, dto.RedactedValue)
}

// TestHandler_PDPDeny_Get_403 verifies that a principal with an Authorizer
// that denies the request receives 403 ERR_AUTH_FORBIDDEN — the PDP deny path.
func TestHandler_PDPDeny_Get_403(t *testing.T) {
	handler, _ := setupHandler()

	ctx := withDenyAuthorizer(
		configcoretest.CtxWithTenant(auth.TestContext("admin-user", []string{auth.RoleAdmin})),
		"policy: deny",
	)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/app.name", nil)
	handler.ServeHTTP(w, req.WithContext(ctx))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// TestHandler_PDPDeny_List_403 verifies that a principal with an Authorizer
// that denies the request receives 403 ERR_AUTH_FORBIDDEN on the list endpoint.
func TestHandler_PDPDeny_List_403(t *testing.T) {
	handler, _ := setupHandler()

	ctx := withDenyAuthorizer(
		configcoretest.CtxWithTenant(auth.TestContext("admin-user", []string{auth.RoleAdmin})),
		"policy: deny",
	)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/", nil)
	handler.ServeHTTP(w, req.WithContext(ctx))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// TestHandler_NoAuthorizer_Get_FailClosed verifies that a principal present in
// ctx but with no Authorizer wired results in fail-closed 403 ERR_AUTH_FORBIDDEN.
func TestHandler_NoAuthorizer_Get_FailClosed(t *testing.T) {
	handler, _ := setupHandler()

	ctx := configcoretest.CtxWithTenant(auth.TestContext("admin-user", []string{auth.RoleAdmin}))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, configBasePath+"/app.name", nil)
	handler.ServeHTTP(w, req.WithContext(ctx))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// TestHandler_ActionPin_ConfigRead pins that every configread endpoint calls the
// PDP with action "config:read" (not another permission). Because the baseline
// grants admin permission for all config actions, a wrong binding
// (e.g. authz.PermConfigWrite()) would still produce 200, so this test uses a
// CapturingAuthorizer to assert the exact action sent to the PDP.
// Failure message: configread gate must use authz.PermConfigRead() ("config:read"),
// not another permission (baseline grants admin for all config perms, masking misbinding).
func TestHandler_ActionPin_ConfigRead(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testReadTenant, &domain.ConfigEntry{
		ID: "cfg-pin", Key: "pin.key", Value: "v", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	endpoints := []struct {
		method string
		target string
	}{
		{http.MethodGet, configBasePath + "/pin.key"},
		{http.MethodGet, configBasePath + "/"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.target, func(t *testing.T) {
			cap := allowAuthorizer()
			ctx := configcoretest.WithAuthorizer(
				configcoretest.CtxWithTenant(auth.TestContext("admin-user", []string{auth.RoleAdmin})),
				cap,
			)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(ep.method, ep.target, nil)
			handler.ServeHTTP(w, req.WithContext(ctx))

			// Gate must pass (2xx) and must have called PDP with "config:read".
			require.Equal(t, http.StatusOK, w.Code,
				"configread gate must pass for allow Authorizer (method=%s path=%s)", ep.method, ep.target)
			assert.Equal(t, "config:read", cap.GotAction,
				"configread gate must use authz.PermConfigRead() (\"config:read\"), not another permission "+
					"(baseline grants admin for all config perms, masking misbinding); endpoint=%s %s",
				ep.method, ep.target)
		})
	}
}
