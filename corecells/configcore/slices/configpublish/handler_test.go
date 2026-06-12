package configpublish

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/corecells/configcore/configcoretest"
	"github.com/ghbvf/gocell/corecells/internal/testoutbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/corecells/configcore/internal/mem"
	configpublishgen "github.com/ghbvf/gocell/generated/contracts/http/config/publish/v1"
	rollbackgen "github.com/ghbvf/gocell/generated/contracts/http/config/rollback/v1"
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

// reason is fixed here (unparam: all configpublish deny tests use the same reason); other slices keep a reason param.
func withDenyAuthorizer(ctx context.Context) context.Context {
	return configcoretest.WithAuthorizer(ctx, &configcoretest.CapturingAuthorizer{Decision: authz.Deny("test pdp deny")})
}

// testPublishTenantStr is the test TenantID string for configpublish tests.
// Derived from configcoretest.TestTenant to avoid duplicating the bare literal.
const testPublishTenantStr = string(configcoretest.TestTenant)

// testPublishTenant is the typed TenantID for direct repo seeding calls.
const testPublishTenant = configcoretest.TestTenant

// adminCtx returns a request context carrying an admin subject + role, a valid
// TenantID, and an allow Authorizer. The Authorizer is required because the gate
// now uses auth.RequirePermission(authz.PermConfigPublish()) — without an
// Authorizer in ctx the gate is fail-closed 403 before reaching the service.
func adminCtx() context.Context {
	base := ctxkeys.WithTenantID(auth.TestContext("test-admin", []string{"admin"}), testPublishTenantStr)
	return withAllowAuthorizer(base)
}

// withAdmin clones req with the admin auth context attached.
func withAdmin(req *http.Request) *http.Request {
	return req.WithContext(adminCtx())
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

func TestToConfigVersionResponse_NilInput(t *testing.T) {
	var got ConfigVersionResponse
	assert.NotPanics(t, func() { got = toConfigVersionResponse(nil) })
	assert.Zero(t, got.ID)
}

func TestConfigVersionResponse_Fields(t *testing.T) {
	now := time.Now()
	version := &domain.ConfigVersion{
		ID: "cv-1", ConfigID: "cfg-1", Version: 3, Value: "v3",
		PublishedAt: &now,
	}
	resp := toConfigVersionResponse(version)

	assert.Equal(t, "cv-1", resp.ID)
	assert.Equal(t, "cfg-1", resp.ConfigID)
	assert.Equal(t, 3, resp.Version)
	assert.Equal(t, "v3", resp.Value)
	require.NotNil(t, resp.PublishedAt)
	assert.Equal(t, now, *resp.PublishedAt)

	// Verify camelCase JSON keys.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"id"`)
	assert.Contains(t, s, `"configId"`)
	assert.Contains(t, s, `"version"`)
	assert.Contains(t, s, `"value"`)
	// configpublish RESPONSE DTO deliberately exposes the `sensitive` flag so
	// callers know to redact UI; this is unrelated to PR-CFG-G2's removal of
	// `sensitive` from contracts/http/config/update/v1/request.schema.json
	// (which deleted the request-side field that handler/service/repo never read).
	assert.Contains(t, s, `"sensitive"`)
	assert.Contains(t, s, `"publishedAt"`)
}

func TestConfigVersionResponse_OmitsNilPublishedAt(t *testing.T) {
	version := &domain.ConfigVersion{
		ID: "cv-2", ConfigID: "cfg-2", Version: 1, Value: "v1",
		PublishedAt: nil,
	}
	resp := toConfigVersionResponse(version)

	b, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"publishedAt"`,
		"nil PublishedAt must be omitted via omitempty")
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
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route(configPrefix, func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
	return mux, repo
}

func seedForPublish(t *testing.T, repo *mem.ConfigRepository) {
	t.Helper()
	const key = "app.name"
	const value = "v1"
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testPublishTenant, &domain.ConfigEntry{
		ID: "cfg-" + key, Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func TestHandler_HandlePublish_OK(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil))
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"publishedAt"`)
	assert.Contains(t, body, `"configId"`)
}

func TestHandler_HandlePublish_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/missing/publish", nil))
	handler.ServeHTTP(w, req)

	errcodetest.AssertWireCode(t, w, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
}

// --- F6: missing-tenant → typed 403 (not 500) ---

// withAdminNoTenant injects an admin principal and an allow Authorizer but NO
// TenantID. The request passes the config:publish-gated (PDP) policy, then fails
// Service.tenant.FromContext. The resulting 403 carries ErrAuthForbidden (distinct
// from the PDP gate's fail-closed 403).
func withAdminNoTenant(req *http.Request) *http.Request {
	base := auth.TestContext("test-admin", []string{"admin"})
	return req.WithContext(withAllowAuthorizer(base))
}

func TestHandler_HandlePublish_MissingTenant_403(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	req := withAdminNoTenant(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil))
	handler.ServeHTTP(w, req)

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_HandleRollback_MissingTenant_403(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := withAdminNoTenant(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback",
		strings.NewReader(`{"version":1,"expectedVersion":1}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// config:publish-gated (PDP) — publish and rollback are high-risk write operations
// that require the config:publish permission from the ABAC PDP.
// 401 path: no principal in ctx → unauthenticated before PDP is consulted.
// 403 deny path: principal present, PDP denies → ErrAuthForbidden.
// 403 no-authorizer path: principal present, no Authorizer in ctx → fail-closed 403.

func TestHandler_HandlePublish_RequiresAuth(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil) // no auth
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "publish without subject must be 401")
}

// TestHandler_HandlePublish_PDPDeny verifies that a PDP deny response causes a 403.
func TestHandler_HandlePublish_PDPDeny(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	ctx := withDenyAuthorizer(
		ctxkeys.WithTenantID(auth.TestContext("user-1", []string{"viewer"}), testPublishTenantStr),
	)
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil).
		WithContext(ctx)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "PDP deny must yield 403")
}

// TestHandler_HandlePublish_NoAuthorizer verifies that absent Authorizer is fail-closed 403.
func TestHandler_HandlePublish_NoAuthorizer(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	// Principal present but no Authorizer in ctx — RequirePermission must deny.
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil).
		WithContext(ctxkeys.WithTenantID(auth.TestContext("user-1", []string{"admin"}), testPublishTenantStr))
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "missing Authorizer must be fail-closed 403")
}

func TestHandler_HandleRollback_RequiresAuth(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback",
		strings.NewReader(`{"version":1,"expectedVersion":1}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "rollback without subject must be 401")
}

// TestHandler_HandleRollback_PDPDeny verifies that a PDP deny response causes a 403.
func TestHandler_HandleRollback_PDPDeny(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	ctx := withDenyAuthorizer(
		ctxkeys.WithTenantID(auth.TestContext("user-1", []string{"viewer"}), testPublishTenantStr),
	)
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback",
		strings.NewReader(`{"version":1,"expectedVersion":1}`)).
		WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "PDP deny must yield 403")
}

// TestHandler_HandleRollback_NoAuthorizer verifies that absent Authorizer is fail-closed 403.
func TestHandler_HandleRollback_NoAuthorizer(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	// Principal present but no Authorizer in ctx — RequirePermission must deny.
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback",
		strings.NewReader(`{"version":1,"expectedVersion":1}`)).
		WithContext(ctxkeys.WithTenantID(auth.TestContext("user-1", []string{"admin"}), testPublishTenantStr))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "missing Authorizer must be fail-closed 403")
}

// H2-2 CONFIGPUBLISH-REDACT-01: sensitive entries must redact `value` and expose
// the `sensitive` flag in the publish response so downstream logs/UI cannot leak the secret.
func TestHandler_HandlePublish_SensitiveRedacted(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testPublishTenant, &domain.ConfigEntry{
		ID: "cfg-secret", Key: "db.password", Value: "s3cret!", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/db.password/publish", nil))
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data struct {
			Value     string `json:"value"`
			Sensitive bool   `json:"sensitive"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "******", resp.Data.Value, "sensitive value must be redacted in publish response")
	assert.True(t, resp.Data.Sensitive, "publish response must surface the sensitive flag")
	assert.NotContains(t, w.Body.String(), "s3cret!", "raw secret must not appear anywhere in the body")
}

func TestHandler_HandlePublish_NonSensitiveVisible(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testPublishTenant, &domain.ConfigEntry{
		ID: "cfg-plain", Key: "app.name", Value: "gocell", Sensitive: false,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil))
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data struct {
			Value     string `json:"value"`
			Sensitive bool   `json:"sensitive"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "gocell", resp.Data.Value, "non-sensitive value must be returned plaintext")
	assert.False(t, resp.Data.Sensitive)
}

func TestHandler_HandleRollback_OK(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)
	// Publish first to create a version.
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)
	_, err = svc.Publish(adminCtx(), "app.name")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	body := `{"version":1,"expectedVersion":1}`
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// PR#155 followup F4 (Cx1, P2): rollback negative-path coverage. Locks 404
// for both missing-key and missing-version inputs so future error-mapping
// regressions surface in CI rather than at runtime.
func TestHandler_HandleRollback_KeyNotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/missing/rollback",
		strings.NewReader(`{"version":1,"expectedVersion":1}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"code"`)
	// PR#155 followup F3: external 404 must not leak repo-internal identifiers.
	assert.NotContains(t, body, "config repo")
}

func TestHandler_HandleRollback_VersionNotFound(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo) // entry exists, but no version published yet

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback",
		strings.NewReader(`{"version":42,"expectedVersion":1}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	body := w.Body.String()
	// PR#155 followup F3: external 404 must not leak the internal config_id or
	// the requested version number (which would help an attacker enumerate).
	assert.NotContains(t, body, "cfg-app.name", "internal config id must not leak in 404")
	assert.NotContains(t, body, "config repo", "internal repo prefix must not leak")
}

// PR#155 review F2: rollback response must redact the value when the snapshot
// was sensitive, mirroring the publish response guarantee.
func TestHandler_HandleRollback_SensitiveRedacted(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), testPublishTenant, &domain.ConfigEntry{
		ID: "cfg-secret", Key: "db.password", Value: "s3cret!", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))
	// Publish v1 carries Sensitive=true into the snapshot.
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)
	_, err = svc.Publish(adminCtx(), "db.password")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/db.password/rollback",
		strings.NewReader(`{"version":1,"expectedVersion":1}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Value     string `json:"value"`
			Sensitive bool   `json:"sensitive"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "******", resp.Data.Value, "rollback response must redact sensitive snapshot value")
	assert.True(t, resp.Data.Sensitive)
	assert.NotContains(t, w.Body.String(), "s3cret!", "raw secret must not appear anywhere in the rollback body")
}

func TestHandler_HandleRollback_UnknownField(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"version":1,"expectedVersion":1,"extra":"y"}`
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleRollback_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback", strings.NewReader("{bad")))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleRollback_InvalidVersion(t *testing.T) {
	tests := []struct {
		name    string
		version int
	}{
		{name: "version 0", version: 0},
		{name: "version -1", version: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, repo := setupHandler()
			seedForPublish(t, repo)

			w := httptest.NewRecorder()
			body := fmt.Sprintf(`{"version":%d,"expectedVersion":1}`, tt.version)
			req := withAdmin(httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback", strings.NewReader(body)))
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			// The generated handler validates version >= 1 (minimum: 1 from contract.yaml)
			// before calling the service, returning ERR_VALIDATION_FAILED.
			assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
		})
	}
}

// --- outbox/tx tests ---

func TestService_WithEmitter(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	ow := &stubOutboxWriter{}
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, ow))), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	seedForService(repo, "k1", "v1")
	_, err = svc.Publish(adminCtx(), "k1")
	require.NoError(t, err)

	assert.Len(t, ow.entries, 1)
	assert.Equal(t, domain.TopicConfigVersionPublished, ow.entries[0].EventType())
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	tx := &stubTxRunner{}
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(tx)))
	require.NoError(t, err)

	seedForService(repo, "k2", "v2")
	_, err = svc.Publish(adminCtx(), "k2")
	require.NoError(t, err)

	assert.Equal(t, 1, tx.calls)
}

func TestService_Rollback_WithOutbox(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	ow := &stubOutboxWriter{}
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, ow))), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	seedForService(repo, "k3", "v3")
	_, err = svc.Publish(adminCtx(), "k3")
	require.NoError(t, err)

	_, err = svc.Rollback(adminCtx(), "k3", 1, 1)
	require.NoError(t, err)

	assert.Len(t, ow.entries, 3, "publish writes version-published; rollback writes state-sync then audit")
	assert.Equal(t, domain.TopicConfigEntryUpserted, ow.entries[1].EventType())
	assert.Equal(t, domain.TopicConfigRollback, ow.entries[2].EventType())
}

func seedForService(repo *mem.ConfigRepository, key, value string) {
	now := time.Now()
	_ = repo.Create(context.Background(), testPublishTenant, &domain.ConfigEntry{
		ID: "cfg-" + key, Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})
}

// ---------------------------------------------------------------------------
// PR464 P2.2: typed 404 / 409 envelope adapter regression coverage.
// fakeConfigRepoForRollback wraps mem.ConfigRepository and overrides
// UpdateForRollback to inject controlled errcode.Error responses, asserting
// RollbackAdapter's errors.As + ce.Code switch returns the typed envelope.
// ---------------------------------------------------------------------------

type fakeConfigRepoForRollback struct {
	*mem.ConfigRepository
	rollbackErr error
}

func (f *fakeConfigRepoForRollback) UpdateForRollback(
	_ context.Context, _ tenant.TenantID, _ string, _ int, _ string, _ bool,
) (*domain.ConfigEntry, error) {
	return nil, f.rollbackErr
}

func newRollbackAdapter(t *testing.T, rollbackErr error) RollbackAdapter {
	t.Helper()
	repo := &fakeConfigRepoForRollback{
		ConfigRepository: mem.NewConfigRepository(clock.Real()),
		rollbackErr:      rollbackErr,
	}
	// Seed an entry + a version snapshot so service-level pre-checks
	// (GetByKey + GetVersion) succeed and we reach UpdateForRollback.
	seedForRollbackAdapter(repo.ConfigRepository, "k-rollback", "v1")
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)
	return RollbackAdapter{S: svc}
}

func seedForRollbackAdapter(repo *mem.ConfigRepository, key, value string) {
	now := time.Now()
	entry := &domain.ConfigEntry{
		ID: "cfg-rollback", Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	_ = repo.Create(context.Background(), testPublishTenant, entry)
	_ = repo.PublishVersion(context.Background(), testPublishTenant, &domain.ConfigVersion{
		ID: "ver-1", ConfigID: entry.ID, Version: 1, Value: value, PublishedAt: &now,
	})
}

func TestRollbackAdapter_NotFound_Returns404Typed(t *testing.T) {
	rollbackAd := newRollbackAdapter(t,
		errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found"))
	resp, err := rollbackAd.Rollback(adminCtx(),
		&rollbackgen.Request{Key: "k-rollback", Version: 1, ExpectedVersion: 1})
	require.NoError(t, err, "adapter must map ErrConfigRepoNotFound to typed 404")
	typed, ok := resp.(rollbackgen.Rollback404ErrorResponse)
	require.True(t, ok, "expected Rollback404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, typed.Body.Code)
}

func TestRollbackAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	rollbackAd := newRollbackAdapter(t,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"))
	resp, err := rollbackAd.Rollback(adminCtx(),
		&rollbackgen.Request{Key: "k-rollback", Version: 1, ExpectedVersion: 1})
	require.NoError(t, err, "adapter must map ErrVersionConflict to typed 409")
	typed, ok := resp.(rollbackgen.Rollback409ErrorResponse)
	require.True(t, ok, "expected Rollback409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}

// ---------------------------------------------------------------------------
// B2-T-08: typed 404 envelope for PublishAdapter — ErrConfigRepoNotFound path.
// PublishAdapter.Publish maps ErrConfigRepoNotFound (the unified not-found code
// returned by both the PG and mem repos) to configpublishgen.Publish404ErrorResponse.
// TestHandler_HandlePublish_NotFound covers the same code via the full HTTP stack.
// This test directly exercises the repo error path by injecting
// ErrConfigRepoNotFound via a fakeConfigRepoForPublish that overrides GetByKey —
// the first repo call in Service.Publish.
// ---------------------------------------------------------------------------

type fakeConfigRepoForPublish struct {
	*mem.ConfigRepository
	getByKeyErr error
}

func (f *fakeConfigRepoForPublish) GetByKey(_ context.Context, _ tenant.TenantID, _ string) (*domain.ConfigEntry, error) {
	return nil, f.getByKeyErr
}

func newPublishAdapter(t *testing.T, getByKeyErr error) PublishAdapter {
	t.Helper()
	repo := &fakeConfigRepoForPublish{
		ConfigRepository: mem.NewConfigRepository(clock.Real()),
		getByKeyErr:      getByKeyErr,
	}
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)
	return PublishAdapter{S: svc}
}

func TestPublishAdapter_RepoNotFound_Returns404Typed(t *testing.T) {
	publishAd := newPublishAdapter(t,
		errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found"))
	resp, err := publishAd.Publish(adminCtx(), &configpublishgen.Request{Key: "k-publish"})
	require.NoError(t, err, "adapter must map ErrConfigRepoNotFound to typed 404")
	typed, ok := resp.(configpublishgen.Publish404ErrorResponse)
	require.True(t, ok, "expected Publish404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, typed.Body.Code)
}

// ---------------------------------------------------------------------------
// Action-pin test (PR-10b #1348): asserts that the configpublish gate asks the
// PDP for exactly "config:publish". Without this guard, an endpoint↔permission
// misbinding (e.g. using PermConfigWrite instead of PermConfigPublish) would be
// masked by the baseline which allows admin for every config:* permission.
// ---------------------------------------------------------------------------

func TestHandler_ConfigPublishGate_ActionPin(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	// Seed a version for rollback to succeed.
	svcForSeed, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)
	_, err = svcForSeed.Publish(adminCtx(), "app.name")
	require.NoError(t, err)

	cap := allowAuthorizer()
	baseCtx := ctxkeys.WithTenantID(auth.TestContext("test-admin", []string{"admin"}), testPublishTenantStr)
	authedCtx := configcoretest.WithAuthorizer(baseCtx, cap)

	t.Run("publish gate asks config:publish", func(t *testing.T) {
		cap.GotAction = "" // reset between sub-tests
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil).
			WithContext(authedCtx)
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code,
			"publish must succeed with allow Authorizer; got body: %s", w.Body.String())
		assert.Equal(t, "config:publish", cap.GotAction,
			"configpublish gate must request config:publish from PDP; a wrong permission would be masked by the admin baseline")
	})

	t.Run("rollback gate asks config:publish", func(t *testing.T) {
		cap.GotAction = "" // reset between sub-tests
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback",
			strings.NewReader(`{"version":1,"expectedVersion":1}`)).
			WithContext(authedCtx)
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code,
			"rollback must succeed with allow Authorizer; got body: %s", w.Body.String())
		assert.Equal(t, "config:publish", cap.GotAction,
			"configpublish rollback gate must request config:publish from PDP")
	})
}
