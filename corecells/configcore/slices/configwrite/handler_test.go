package configwrite

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/corecells/internal/testoutbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/corecells/configcore/internal/mem"
	configdelete "github.com/ghbvf/gocell/generated/contracts/http/config/delete/v1"
	update "github.com/ghbvf/gocell/generated/contracts/http/config/update/v1"
	write "github.com/ghbvf/gocell/generated/contracts/http/config/write/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

// testHandlerTenantStr is the test tenant injected into request contexts so
// configwrite.Service.Create/Update/Delete can call tenant.FromContext without
// error. Mirrors the pattern established by accesscore PR-2a.
const testHandlerTenantStr = "00000000-0000-0000-0000-000000000001"

// --- local authz test helpers ---
// configcoretest cannot be imported here (cycle: configcoretest imports configwrite).
// These local stubs are semantically identical to configcoretest's equivalents.

// capturingAuthorizer is a test-only auth.Authorizer that returns a fixed
// Decision and records the action of the last Authorize call.
type capturingAuthorizer struct {
	decision  authz.Decision
	GotAction string
}

func (c *capturingAuthorizer) Authorize(_ context.Context, _, _, action string) (authz.Decision, error) {
	c.GotAction = action
	return c.decision, nil
}

// newAllowAuthorizer returns a capturingAuthorizer that grants every request.
func newAllowAuthorizer() *capturingAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("newAllowAuthorizer: " + err.Error())
	}
	return &capturingAuthorizer{decision: dec}
}

// withAllowAuthorizer wraps ctx with an allow-all Authorizer.
func withAllowAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, newAllowAuthorizer())
}

// withDenyAuthorizer wraps ctx with a deny-all Authorizer (PDP-deny → 403 path).
func withDenyAuthorizer(ctx context.Context, reason string) context.Context {
	return auth.WithAuthorizer(ctx, &capturingAuthorizer{decision: authz.Deny(reason)})
}

// testHandlerTenant is the typed TenantID used for direct repo.Create seeding
// in handler tests.
var testHandlerTenant = tenant.TenantID(testHandlerTenantStr)

const testAdminSubject = "admin-test"

// adminTestCtx returns a context with an admin principal and the canonical test
// tenant injected, suitable for service-level calls in handler tests.
func adminTestCtx() context.Context {
	return ctxkeys.WithTenantID(auth.TestContext("test-admin", []string{"admin"}), testHandlerTenantStr)
}

// --- stubs ---

type stubOutboxWriter struct{ entries []outbox.Entry }

func (s *stubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

type stubTxRunner struct{ calls int }

func (s *stubTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(ctx)
}

// withAdmin injects an admin context, a valid TenantID, and an allow Authorizer
// into a request so it passes the auth.RequirePermission(authz.PermConfigWrite())
// PDP gate and configwrite.Service methods can call tenant.FromContext without error.
func withAdmin(req *http.Request) *http.Request {
	ctx := withAllowAuthorizer(
		ctxkeys.WithTenantID(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr),
	)
	return req.WithContext(ctx)
}

// --- handler tests ---

// configPrefix matches cell-level Route("/api/v1/config", ...).
const configPrefix = "/api/v1/config"

func setupHandler() (http.Handler, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository(clock.Real())
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	if err != nil {
		panic("setupHandler: " + err.Error())
	}
	policy := auth.RequirePermission(authz.PermConfigWrite())
	writeH := write.NewHandler(WriteAdapter{svc}, policy)
	updateH := update.NewHandler(UpdateAdapter{svc}, policy)
	deleteH := configdelete.NewHandler(DeleteAdapter{svc}, policy)
	mux := celltest.NewTestMux()
	mux.Route(configPrefix, func(sub cell.RouteMux) {
		if err := writeH.RegisterRoutes(sub); err != nil {
			panic("write.RegisterRoutes: " + err.Error())
		}
		if err := updateH.RegisterRoutes(sub); err != nil {
			panic("update.RegisterRoutes: " + err.Error())
		}
		if err := deleteH.RegisterRoutes(sub); err != nil {
			panic("delete.RegisterRoutes: " + err.Error())
		}
	})
	return mux, repo
}

func TestHandler_HandleCreate_OK(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"gocell"}`
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "app.name")
}

func TestHandler_HandleCreate_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleCreate_EmptyKey(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"","value":"v"}`
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandler_Create_BlankKey verifies that submitting an empty key returns
// 400 + ERR_CONFIG_INVALID_INPUT + "key is required" so the client-visible
// message uses the JSON body field name, not a Go identifier.
func TestHandler_Create_BlankKey(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"","value":"v"}`
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// The generated handler validates minLength before calling the service,
	// returning ERR_VALIDATION_FAILED for an empty key (minLength: 1).
	assert.Equal(t, "ERR_VALIDATION_FAILED", resp.Error.Code)
}

func TestHandler_HandleCreate_UnknownField(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"gocell","extra":"y"}`
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleUpdate_UnknownField(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"value":"new","expectedVersion":1,"extra":"y"}`
	req := httptest.NewRequest(http.MethodPut, configPrefix+"/k", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleUpdate_OK(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testHandlerTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "old",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	body := `{"value":"new","expectedVersion":1}`
	req := httptest.NewRequest(http.MethodPut, configPrefix+"/app.name", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "new")
}

func TestHandler_HandleUpdate_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"value":"v","expectedVersion":1}`
	req := httptest.NewRequest(http.MethodPut, configPrefix+"/missing", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	errcodetest.AssertWireCode(t, w, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
}

func TestHandler_HandleUpdate_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, configPrefix+"/k", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleDelete_OK(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testHandlerTenant, &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "v",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, configPrefix+"/app.name?expectedVersion=1", nil)
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandler_HandleDelete_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, configPrefix+"/missing?expectedVersion=1", nil)
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	errcodetest.AssertWireCode(t, w, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
}

// --- F6: missing-tenant → typed 403 (not 500) ---

// withAdminNoTenant injects an admin principal and an allow Authorizer but NO
// TenantID, so the request passes the PDP gate yet fails Service.tenant.FromContext.
func withAdminNoTenant(req *http.Request) *http.Request {
	ctx := withAllowAuthorizer(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}))
	return req.WithContext(ctx)
}

func TestHandler_HandleCreate_MissingTenant_403(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"gocell"}`
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withAdminNoTenant(req))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_HandleUpdate_MissingTenant_403(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"value":"v","expectedVersion":1}`
	req := httptest.NewRequest(http.MethodPut, configPrefix+"/app.name", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withAdminNoTenant(req))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_HandleDelete_MissingTenant_403(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, configPrefix+"/app.name?expectedVersion=1", nil)
	handler.ServeHTTP(w, withAdminNoTenant(req))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// --- sensitive value redaction tests (#27o) ---

func TestHandler_HandleCreate_SensitiveRedacted(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"db.password","value":"s3cret!","sensitive":true}`
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp struct {
		Data write.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "******", resp.Data.Value, "sensitive value must be redacted in response")
	assert.True(t, resp.Data.Sensitive)
	assert.NotContains(t, w.Body.String(), "s3cret!")
}

func TestHandler_HandleUpdate_SensitiveRedacted(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testHandlerTenant, &domain.ConfigEntry{
		ID: "cfg-s1", Key: "api.key", Value: "old-secret", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	body := `{"value":"new-secret","expectedVersion":1}`
	req := httptest.NewRequest(http.MethodPut, configPrefix+"/api.key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data update.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "******", resp.Data.Value, "sensitive value must be redacted in update response")
	assert.NotContains(t, w.Body.String(), "new-secret")
}

// TestService_Create_SensitiveEventPayloadMetadataOnly verifies that the event
// payload for a sensitive entry carries only metadata (key+version) — no value
// field at all. Metadata-only model eliminates redaction entirely by not
// including the value in the event. Subscribers must refetch via GET /api/v1/config/{key}.
func TestService_Create_SensitiveEventPayloadMetadataOnly(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	ow := &stubOutboxWriter{}
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, ow))), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	_, err = svc.Create(ctxkeys.WithTenantID(auth.TestContext("test-admin", []string{"admin"}), testHandlerTenantStr), CreateInput{
		Key: "db.password", Value: "s3cret!", Sensitive: true,
	})
	require.NoError(t, err)

	require.Len(t, ow.entries, 1)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(ow.entries[0].Payload(), &payload))
	assert.NotContains(t, payload, "value", "metadata-only event must not contain value field")
	assert.NotContains(t, payload, "sensitive", "state-sync events must not expose sensitive classification")
	assert.Equal(t, "db.password", payload["key"])
	assert.Equal(t, float64(1), payload["version"])
}

// --- outbox/tx service tests ---

func TestService_WithEmitter(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	ow := &stubOutboxWriter{}
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, ow))), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	_, err = svc.Create(adminTestCtx(), CreateInput{Key: "k1", Value: "v1"})
	require.NoError(t, err)

	assert.Len(t, ow.entries, 1, "outbox writer should receive one entry")
	assert.Equal(t, domain.TopicConfigEntryUpserted, ow.entries[0].EventType())
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	tx := &stubTxRunner{}
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(tx)))
	require.NoError(t, err)

	_, err = svc.Create(adminTestCtx(), CreateInput{Key: "k1", Value: "v1"})
	require.NoError(t, err)

	assert.Equal(t, 1, tx.calls, "tx runner should be called once")
}

func TestService_WithOutboxAndTx(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, ow))), WithTxManager(persistence.WrapForCell(tx)))
	require.NoError(t, err)

	// Create
	_, err = svc.Create(adminTestCtx(), CreateInput{Key: "k1", Value: "v1"})
	require.NoError(t, err)

	// Update
	_, err = svc.Update(adminTestCtx(), UpdateInput{Key: "k1", Value: "v2", ExpectedVersion: 1})
	require.NoError(t, err)

	// Delete
	err = svc.Delete(adminTestCtx(), "k1", 2)
	require.NoError(t, err)

	assert.Equal(t, 3, tx.calls, "each op should use tx")
	assert.Len(t, ow.entries, 3, "each op should write to outbox")
}

// --- authz tests ---

// authzCtx builds a context for handler authz table tests:
//   - no principal (injectAuth=false): bare background ctx → 401.
//   - no_authorizer (injectAuth=true, allowPDP=false): principal + tenant, no
//     Authorizer → fail-closed 403 (tests the PDP-not-wired path).
//   - allow (injectAuth=true, allowPDP=true): principal + tenant + allow
//     Authorizer → gate passes, business logic runs.
func authzCtx(subject string, roles []string, injectAuth, allowPDP bool) context.Context {
	if !injectAuth {
		return context.Background()
	}
	ctx := ctxkeys.WithTenantID(auth.TestContext(subject, roles), testHandlerTenantStr)
	if allowPDP {
		ctx = withAllowAuthorizer(ctx)
	}
	return ctx
}

func TestHandler_Authz_Create(t *testing.T) {
	cases := []struct {
		name        string
		subject     string
		roles       []string
		injectAuth  bool
		allowPDP    bool
		wantStatus  int
		wantErrCode string
	}{
		{"no_auth", "", nil, false, false, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"no_authorizer_fail_closed", "user-1", []string{"viewer"}, true, false, http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
		{"admin_allow", testAdminSubject, []string{auth.RoleAdmin}, true, true, http.StatusCreated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := setupHandler()
			body := `{"key":"test.key","value":"v"}`
			req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(authzCtx(tc.subject, tc.roles, tc.injectAuth, tc.allowPDP))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantErrCode != "" {
				var resp struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tc.wantErrCode, resp.Error.Code)
			}
		})
	}
}

func TestHandler_Authz_Update(t *testing.T) {
	cases := []struct {
		name        string
		subject     string
		roles       []string
		injectAuth  bool
		allowPDP    bool
		setup       func(*mem.ConfigRepository)
		path        string
		wantStatus  int
		wantErrCode string
	}{
		{"no_auth", "", nil, false, false, nil, "/nonexistent", http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"no_authorizer_fail_closed", "user-1", []string{"viewer"}, true, false, nil, "/nonexistent", http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
		{"admin_not_found", testAdminSubject, []string{auth.RoleAdmin}, true, true, nil, "/nonexistent", http.StatusNotFound, ""},
		{"admin_success", testAdminSubject, []string{auth.RoleAdmin}, true, true, func(r *mem.ConfigRepository) {
			now := time.Now()
			_ = r.Create(context.Background(), testHandlerTenant, &domain.ConfigEntry{
				ID: "au-1", Key: "test.update", Value: "v", Version: 1, CreatedAt: now, UpdatedAt: now,
			})
		}, "/test.update", http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, repo := setupHandler()
			if tc.setup != nil {
				tc.setup(repo)
			}
			body := `{"value":"new","expectedVersion":1}`
			req := httptest.NewRequest(http.MethodPut, configPrefix+tc.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(authzCtx(tc.subject, tc.roles, tc.injectAuth, tc.allowPDP))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantErrCode != "" {
				var resp struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tc.wantErrCode, resp.Error.Code)
			}
		})
	}
}

func TestHandler_Authz_Delete(t *testing.T) {
	cases := []struct {
		name        string
		subject     string
		roles       []string
		injectAuth  bool
		allowPDP    bool
		setup       func(*mem.ConfigRepository)
		path        string
		wantStatus  int
		wantErrCode string
	}{
		{"no_auth", "", nil, false, false, nil, "/nonexistent?expectedVersion=1", http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{
			"no_authorizer_fail_closed", "user-1",
			[]string{"viewer"},
			true, false, nil, "/nonexistent?expectedVersion=1", http.StatusForbidden, "ERR_AUTH_FORBIDDEN",
		},
		{
			"admin_not_found", testAdminSubject,
			[]string{auth.RoleAdmin},
			true, true, nil, "/nonexistent?expectedVersion=1", http.StatusNotFound, "",
		},
		{"admin_success", testAdminSubject, []string{auth.RoleAdmin}, true, true, func(r *mem.ConfigRepository) {
			now := time.Now()
			_ = r.Create(context.Background(), testHandlerTenant, &domain.ConfigEntry{
				ID: "ad-1", Key: "test.delete", Value: "v", Version: 1, CreatedAt: now, UpdatedAt: now,
			})
		}, "/test.delete?expectedVersion=1", http.StatusNoContent, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, repo := setupHandler()
			if tc.setup != nil {
				tc.setup(repo)
			}
			req := httptest.NewRequest(http.MethodDelete, configPrefix+tc.path, nil)
			req = req.WithContext(authzCtx(tc.subject, tc.roles, tc.injectAuth, tc.allowPDP))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantErrCode != "" {
				var resp struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tc.wantErrCode, resp.Error.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PR464 P2.2: typed 404 / 409 envelope adapter regression coverage.
// fakeConfigRepoErr wraps mem.ConfigRepository and overrides Update/Delete
// to inject controlled errcode.Error responses, asserting the adapter's
// errors.As + ce.Code switch returns the typed envelope (not framework
// fallback) so codegen-declared status codes stay locked.
// ---------------------------------------------------------------------------

type fakeConfigRepoErr struct {
	*mem.ConfigRepository
	updateErr error
	deleteErr error
}

func (f *fakeConfigRepoErr) Update(_ context.Context, _ tenant.TenantID, _ string, _ int, _ string) (*domain.ConfigEntry, error) {
	return nil, f.updateErr
}

func (f *fakeConfigRepoErr) Delete(_ context.Context, _ tenant.TenantID, _ string, _ int) (*domain.ConfigEntry, error) {
	return nil, f.deleteErr
}

func newConfigwriteAdapterUnderTest(t *testing.T, updateErr, deleteErr error) (UpdateAdapter, DeleteAdapter) {
	t.Helper()
	repo := &fakeConfigRepoErr{
		ConfigRepository: mem.NewConfigRepository(clock.Real()),
		updateErr:        updateErr,
		deleteErr:        deleteErr,
	}
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)
	return UpdateAdapter{S: svc}, DeleteAdapter{S: svc}
}

func TestUpdateAdapter_NotFound_Returns404Typed(t *testing.T) {
	updateAd, _ := newConfigwriteAdapterUnderTest(t,
		errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found"),
		nil)
	resp, err := updateAd.Update(ctxkeys.WithTenantID(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr),
		&update.Request{Key: "missing.key", Value: "v", ExpectedVersion: 1})
	require.NoError(t, err, "adapter must map ErrConfigRepoNotFound to typed 404 (not framework fallback)")
	typed, ok := resp.(update.Update404ErrorResponse)
	require.True(t, ok, "expected Update404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, typed.Body.Code)
}

func TestUpdateAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	updateAd, _ := newConfigwriteAdapterUnderTest(t,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"),
		nil)
	resp, err := updateAd.Update(ctxkeys.WithTenantID(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr),
		&update.Request{Key: "stale.key", Value: "v", ExpectedVersion: 1})
	require.NoError(t, err, "adapter must map ErrVersionConflict to typed 409")
	typed, ok := resp.(update.Update409ErrorResponse)
	require.True(t, ok, "expected Update409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}

func TestDeleteAdapter_NotFound_Returns404Typed(t *testing.T) {
	_, deleteAd := newConfigwriteAdapterUnderTest(t, nil,
		errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found"))
	resp, err := deleteAd.Delete(ctxkeys.WithTenantID(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr),
		&configdelete.Request{Key: "missing.key", ExpectedVersion: 1})
	require.NoError(t, err)
	typed, ok := resp.(configdelete.Delete404ErrorResponse)
	require.True(t, ok, "expected Delete404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, typed.Body.Code)
}

func TestDeleteAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	_, deleteAd := newConfigwriteAdapterUnderTest(t, nil,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"))
	resp, err := deleteAd.Delete(ctxkeys.WithTenantID(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr),
		&configdelete.Request{Key: "stale.key", ExpectedVersion: 1})
	require.NoError(t, err)
	typed, ok := resp.(configdelete.Delete409ErrorResponse)
	require.True(t, ok, "expected Delete409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}

// --- PDP-deny and fail-closed tests ---

// TestHandler_PDPDeny_Write_403 verifies that a principal with a deny Authorizer
// receives 403 ERR_AUTH_FORBIDDEN on the write (POST) endpoint.
func TestHandler_PDPDeny_Write_403(t *testing.T) {
	handler, _ := setupHandler()

	ctx := withDenyAuthorizer(
		ctxkeys.WithTenantID(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr),
		"policy: deny",
	)
	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"v"}`
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req.WithContext(ctx))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// TestHandler_NoAuthorizer_Write_FailClosed verifies that a principal present in
// ctx but with no Authorizer wired results in fail-closed 403 ERR_AUTH_FORBIDDEN.
func TestHandler_NoAuthorizer_Write_FailClosed(t *testing.T) {
	handler, _ := setupHandler()

	ctx := ctxkeys.WithTenantID(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr)
	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"v"}`
	req := httptest.NewRequest(http.MethodPost, configPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req.WithContext(ctx))

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// TestHandler_ActionPin_ConfigWrite pins that every configwrite endpoint calls the
// PDP with action "config:write" (not another permission). Because the baseline
// grants admin permission for all config actions, a wrong binding
// (e.g. authz.PermConfigRead()) would still produce 2xx, so this test uses a
// CapturingAuthorizer to assert the exact action sent to the PDP.
// Failure message: configwrite gate must use authz.PermConfigWrite() ("config:write"),
// not another permission (baseline grants admin for all config perms, masking misbinding).
func TestHandler_ActionPin_ConfigWrite(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testHandlerTenant, &domain.ConfigEntry{
		ID: "cfg-pin", Key: "pin.key", Value: "v", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	endpoints := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, configPrefix, `{"key":"action.pin","value":"v"}`},
		{http.MethodPut, configPrefix + "/pin.key", `{"value":"new","expectedVersion":1}`},
		{http.MethodDelete, configPrefix + "/pin.key?expectedVersion=1", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.target, func(t *testing.T) {
			// Re-create repo state for each sub-test since DELETE removes the entry.
			h, r := setupHandler()
			require.NoError(t, r.Create(context.Background(), testHandlerTenant, &domain.ConfigEntry{
				ID: "cfg-pin2", Key: "pin.key", Value: "v", Version: 1,
				CreatedAt: now, UpdatedAt: now,
			}))
			_ = handler // silence unused warning; sub-test uses h
			_ = repo

			cap := newAllowAuthorizer()
			ctx := auth.WithAuthorizer(
				ctxkeys.WithTenantID(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr),
				cap,
			)
			var bodyReader *strings.Reader
			if ep.body != "" {
				bodyReader = strings.NewReader(ep.body)
			} else {
				bodyReader = strings.NewReader("")
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(ep.method, ep.target, bodyReader)
			if ep.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			h.ServeHTTP(w, req.WithContext(ctx))

			// Gate must pass (not 401/403).
			require.NotEqual(t, http.StatusUnauthorized, w.Code,
				"configwrite gate must not block admin with allow Authorizer (method=%s path=%s)", ep.method, ep.target)
			require.NotEqual(t, http.StatusForbidden, w.Code,
				"configwrite gate must not deny admin with allow Authorizer (method=%s path=%s)", ep.method, ep.target)
			assert.Equal(t, "config:write", cap.GotAction,
				"configwrite gate must use authz.PermConfigWrite() (\"config:write\"), not another permission "+
					"(baseline grants admin for all config perms, masking misbinding); endpoint=%s %s",
				ep.method, ep.target)
		})
	}
}
