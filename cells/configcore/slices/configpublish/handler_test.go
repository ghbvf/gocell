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

	"github.com/ghbvf/gocell/cells/internal/testoutbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	rollbackgen "github.com/ghbvf/gocell/generated/contracts/http/config/rollback/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// adminCtx returns a request context carrying an admin subject + role for
// authorized handler tests. Mirrors the identitymanage handler test pattern.
func adminCtx() context.Context {
	return auth.TestContext("test-admin", []string{"admin"})
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
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
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
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
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

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// PR#155 followup F1 (Cx2, P1): publish + rollback are high-risk write operations
// that must require an explicit admin role. Authentication alone (any logged-in
// subject) is not enough — fail-closed at the handler layer mirrors
// identitymanage/handler.go and matches the K8s/Kratos/go-zero default-deny convention.
func TestHandler_HandlePublish_RequiresAuth(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil) // no auth
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "publish without subject must be 401")
}

func TestHandler_HandlePublish_RequiresAdminRole(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/publish", nil).
		WithContext(auth.TestContext("user-1", []string{"viewer"}))
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "non-admin subject must be 403")
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

func TestHandler_HandleRollback_RequiresAdminRole(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, configPrefix+"/app.name/rollback",
		strings.NewReader(`{"version":1,"expectedVersion":1}`)).
		WithContext(auth.TestContext("user-1", []string{"viewer"}))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "non-admin subject must be 403")
}

// H2-2 CONFIGPUBLISH-REDACT-01: sensitive entries must redact `value` and expose
// the `sensitive` flag in the publish response so downstream logs/UI cannot leak the secret.
func TestHandler_HandlePublish_SensitiveRedacted(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
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
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
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
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
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
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-secret", Key: "db.password", Value: "s3cret!", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))
	// Publish v1 carries Sensitive=true into the snapshot.
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
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
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, ow)), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	seedForService(repo, "k1", "v1")
	_, err = svc.Publish(adminCtx(), "k1")
	require.NoError(t, err)

	assert.Len(t, ow.entries, 1)
	assert.Equal(t, domain.TopicConfigVersionPublished, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	tx := &stubTxRunner{}
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(tx)))
	require.NoError(t, err)

	seedForService(repo, "k2", "v2")
	_, err = svc.Publish(adminCtx(), "k2")
	require.NoError(t, err)

	assert.Equal(t, 1, tx.calls)
}

func TestService_Rollback_WithOutbox(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	ow := &stubOutboxWriter{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, ow)), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)

	seedForService(repo, "k3", "v3")
	_, err = svc.Publish(adminCtx(), "k3")
	require.NoError(t, err)

	_, err = svc.Rollback(adminCtx(), "k3", 1, 1)
	require.NoError(t, err)

	assert.Len(t, ow.entries, 3, "publish writes version-published; rollback writes state-sync then audit")
	assert.Equal(t, domain.TopicConfigEntryUpserted, ow.entries[1].EventType)
	assert.Equal(t, domain.TopicConfigRollback, ow.entries[2].EventType)
}

func seedForService(repo *mem.ConfigRepository, key, value string) {
	now := time.Now()
	_ = repo.Create(context.Background(), &domain.ConfigEntry{
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

func (f *fakeConfigRepoForRollback) UpdateForRollback(_ context.Context, _ string, _ int, _ string, _ bool) (*domain.ConfigEntry, error) {
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
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(&stubTxRunner{})))
	require.NoError(t, err)
	return RollbackAdapter{S: svc}
}

func seedForRollbackAdapter(repo *mem.ConfigRepository, key, value string) {
	now := time.Now()
	entry := &domain.ConfigEntry{
		ID: "cfg-rollback", Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	_ = repo.Create(context.Background(), entry)
	_ = repo.PublishVersion(context.Background(), &domain.ConfigVersion{
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
