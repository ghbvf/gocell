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

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func (s *stubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(context.Background())
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
	// PR#155 review F3: lock the sensitive JSON tag so removing it would fail here.
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

func setupHandler() (http.Handler, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	svc := NewService(repo, eventbus.New(), slog.Default())
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{key}/publish", h.HandlePublish)
	mux.HandleFunc("POST /{key}/rollback", h.HandleRollback)
	return mux, repo
}

func seedForPublish(t *testing.T, repo *mem.ConfigRepository, key, value string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-" + key, Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func TestHandler_HandlePublish_OK(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo, "app.name", "v1")

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/app.name/publish", nil))
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"publishedAt"`)
	assert.Contains(t, body, `"configId"`)
}

func TestHandler_HandlePublish_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/missing/publish", nil))
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// PR#155 followup F1 (Cx2, P1): publish + rollback are high-risk write operations
// that must require an explicit admin role. Authentication alone (any logged-in
// subject) is not enough — fail-closed at the handler layer mirrors
// identitymanage/handler.go and matches the K8s/Kratos/go-zero default-deny convention.
func TestHandler_HandlePublish_RequiresAuth(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo, "app.name", "v1")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app.name/publish", nil) // no auth
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "publish without subject must be 401")
}

func TestHandler_HandlePublish_RequiresAdminRole(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo, "app.name", "v1")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app.name/publish", nil).
		WithContext(auth.TestContext("user-1", []string{"viewer"}))
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "non-admin subject must be 403")
}

func TestHandler_HandleRollback_RequiresAuth(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app.name/rollback",
		strings.NewReader(`{"version":1}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "rollback without subject must be 401")
}

func TestHandler_HandleRollback_RequiresAdminRole(t *testing.T) {
	handler, repo := setupHandler()
	seedForPublish(t, repo, "app.name", "v1")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app.name/rollback",
		strings.NewReader(`{"version":1}`)).
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
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/db.password/publish", nil))
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

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
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/app.name/publish", nil))
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

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
	seedForPublish(t, repo, "app.name", "v1")
	// Publish first to create a version.
	svc := NewService(repo, eventbus.New(), slog.Default())
	_, err := svc.Publish(context.Background(), "app.name")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	body := `{"version":1}`
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/app.name/rollback", strings.NewReader(body)))
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
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/missing/rollback",
		strings.NewReader(`{"version":1}`)))
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
	seedForPublish(t, repo, "app.name", "v1") // entry exists, but no version published yet

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/app.name/rollback",
		strings.NewReader(`{"version":42}`)))
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
	svc := NewService(repo, eventbus.New(), slog.Default())
	_, err := svc.Publish(context.Background(), "db.password")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/db.password/rollback",
		strings.NewReader(`{"version":1}`)))
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
	body := `{"version":1,"extra":"y"}`
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/app.name/rollback", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleRollback_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/app.name/rollback", strings.NewReader("{bad")))
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
			seedForPublish(t, repo, "app.name", "v1")

			w := httptest.NewRecorder()
			body := fmt.Sprintf(`{"version":%d}`, tt.version)
			req := withAdmin(httptest.NewRequest(http.MethodPost, "/app.name/rollback", strings.NewReader(body)))
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "rollback target version")
		})
	}
}

// --- outbox/tx tests ---

func TestService_WithOutboxWriter(t *testing.T) {
	repo := mem.NewConfigRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(repo, eventbus.New(), slog.Default(), WithOutboxWriter(ow))

	seedForService(repo, "k1", "v1")
	_, err := svc.Publish(context.Background(), "k1")
	require.NoError(t, err)

	assert.Len(t, ow.entries, 1)
	assert.Equal(t, TopicConfigChanged, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewConfigRepository()
	tx := &stubTxRunner{}
	svc := NewService(repo, eventbus.New(), slog.Default(), WithTxManager(tx))

	seedForService(repo, "k2", "v2")
	_, err := svc.Publish(context.Background(), "k2")
	require.NoError(t, err)

	assert.Equal(t, 1, tx.calls)
}

func TestService_Rollback_WithOutbox(t *testing.T) {
	repo := mem.NewConfigRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(repo, eventbus.New(), slog.Default(), WithOutboxWriter(ow))

	seedForService(repo, "k3", "v3")
	_, err := svc.Publish(context.Background(), "k3")
	require.NoError(t, err)

	_, err = svc.Rollback(context.Background(), "k3", 1)
	require.NoError(t, err)

	assert.Len(t, ow.entries, 2, "publish + rollback should each write to outbox")
	assert.Equal(t, TopicConfigRollback, ow.entries[1].EventType)
}

func seedForService(repo *mem.ConfigRepository, key, value string) {
	now := time.Now()
	_ = repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-" + key, Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})
}
