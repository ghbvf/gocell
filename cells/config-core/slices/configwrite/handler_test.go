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

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/dto"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAdminSubject = "admin-test"

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

// withAdmin injects an admin context into a request for tests that exercise
// non-auth logic (e.g. validation, business errors) and need to pass the
// auth guard.
func withAdmin(req *http.Request) *http.Request {
	return req.WithContext(auth.TestContext(testAdminSubject, []string{dto.RoleAdmin}))
}

// --- handler tests ---

func setupHandler() (http.Handler, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	svc := NewService(repo, eventbus.New(), slog.Default())
	h := NewHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, repo
}

func TestHandler_HandleCreate_OK(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"gocell"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "app.name")
}

func TestHandler_HandleCreate_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleCreate_EmptyKey(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"","value":"v"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleCreate_UnknownField(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"gocell","extra":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleUpdate_UnknownField(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"value":"new","extra":"y"}`
	req := httptest.NewRequest(http.MethodPut, "/k", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleUpdate_OK(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "old",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	body := `{"value":"new"}`
	req := httptest.NewRequest(http.MethodPut, "/app.name", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "new")
}

func TestHandler_HandleUpdate_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"value":"v"}`
	req := httptest.NewRequest(http.MethodPut, "/missing", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_HandleUpdate_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/k", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleDelete_OK(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "v",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/app.name", nil)
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandler_HandleDelete_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/missing", nil)
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- sensitive value redaction tests (#27o) ---

func TestHandler_HandleCreate_SensitiveRedacted(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"db.password","value":"s3cret!","sensitive":true}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp struct {
		Data dto.ConfigEntryResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, dto.RedactedValue, resp.Data.Value, "sensitive value must be redacted in response")
	assert.True(t, resp.Data.Sensitive)
	assert.NotContains(t, w.Body.String(), "s3cret!")
}

func TestHandler_HandleUpdate_SensitiveRedacted(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-s1", Key: "api.key", Value: "old-secret", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	body := `{"value":"new-secret"}`
	req := httptest.NewRequest(http.MethodPut, "/api.key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdmin(req)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data dto.ConfigEntryResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, dto.RedactedValue, resp.Data.Value, "sensitive value must be redacted in update response")
	assert.NotContains(t, w.Body.String(), "new-secret")
}

func TestService_Create_SensitiveEventPayloadRedacted(t *testing.T) {
	repo := mem.NewConfigRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(repo, eventbus.New(), slog.Default(), WithOutboxWriter(ow))

	_, err := svc.Create(context.Background(), CreateInput{
		Key: "db.password", Value: "s3cret!", Sensitive: true,
	})
	require.NoError(t, err)

	require.Len(t, ow.entries, 1)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(ow.entries[0].Payload, &payload))
	assert.Equal(t, "******", payload["value"], "sensitive value must be redacted in event payload")
	assert.NotEqual(t, "s3cret!", payload["value"])
}

// --- outbox/tx service tests ---

func TestService_WithOutboxWriter(t *testing.T) {
	repo := mem.NewConfigRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(repo, eventbus.New(), slog.Default(), WithOutboxWriter(ow))

	_, err := svc.Create(context.Background(), CreateInput{Key: "k1", Value: "v1"})
	require.NoError(t, err)

	assert.Len(t, ow.entries, 1, "outbox writer should receive one entry")
	assert.Equal(t, TopicConfigChanged, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewConfigRepository()
	tx := &stubTxRunner{}
	svc := NewService(repo, eventbus.New(), slog.Default(), WithTxManager(tx))

	_, err := svc.Create(context.Background(), CreateInput{Key: "k1", Value: "v1"})
	require.NoError(t, err)

	assert.Equal(t, 1, tx.calls, "tx runner should be called once")
}

func TestService_WithOutboxAndTx(t *testing.T) {
	repo := mem.NewConfigRepository()
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc := NewService(repo, eventbus.New(), slog.Default(),
		WithOutboxWriter(ow), WithTxManager(tx))

	// Create
	_, err := svc.Create(context.Background(), CreateInput{Key: "k1", Value: "v1"})
	require.NoError(t, err)

	// Update
	_, err = svc.Update(context.Background(), UpdateInput{Key: "k1", Value: "v2"})
	require.NoError(t, err)

	// Delete
	err = svc.Delete(context.Background(), "k1")
	require.NoError(t, err)

	assert.Equal(t, 3, tx.calls, "each op should use tx")
	assert.Len(t, ow.entries, 3, "each op should write to outbox")
}

// --- authz tests ---

func TestHandler_Authz_Create(t *testing.T) {
	cases := []struct {
		name        string
		subject     string
		roles       []string
		injectAuth  bool
		wantStatus  int
		wantErrCode string
	}{
		{"no_auth", "", nil, false, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", "user-1", []string{"viewer"}, true, http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
		{"admin", testAdminSubject, []string{dto.RoleAdmin}, true, http.StatusCreated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := setupHandler()
			body := `{"key":"test.key","value":"v"}`
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.injectAuth {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
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
		setup       func(*mem.ConfigRepository)
		path        string
		wantStatus  int
		wantErrCode string
	}{
		{"no_auth", "", nil, false, nil, "/nonexistent", http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", "user-1", []string{"viewer"}, true, nil, "/nonexistent", http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
		{"admin", testAdminSubject, []string{dto.RoleAdmin}, true, nil, "/nonexistent", http.StatusNotFound, ""},
		{"admin_success", testAdminSubject, []string{dto.RoleAdmin}, true, func(r *mem.ConfigRepository) {
			now := time.Now()
			_ = r.Create(context.Background(), &domain.ConfigEntry{
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
			body := `{"value":"new"}`
			req := httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.injectAuth {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
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
		setup       func(*mem.ConfigRepository)
		path        string
		wantStatus  int
		wantErrCode string
	}{
		{"no_auth", "", nil, false, nil, "/nonexistent", http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED"},
		{"non_admin", "user-1", []string{"viewer"}, true, nil, "/nonexistent", http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
		{"admin", testAdminSubject, []string{dto.RoleAdmin}, true, nil, "/nonexistent", http.StatusNotFound, ""},
		{"admin_success", testAdminSubject, []string{dto.RoleAdmin}, true, func(r *mem.ConfigRepository) {
			now := time.Now()
			_ = r.Create(context.Background(), &domain.ConfigEntry{
				ID: "ad-1", Key: "test.delete", Value: "v", Version: 1, CreatedAt: now, UpdatedAt: now,
			})
		}, "/test.delete", http.StatusNoContent, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, repo := setupHandler()
			if tc.setup != nil {
				tc.setup(repo)
			}
			req := httptest.NewRequest(http.MethodDelete, tc.path, nil)
			if tc.injectAuth {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
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
