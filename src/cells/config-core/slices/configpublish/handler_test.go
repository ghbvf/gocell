package configpublish

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	req := httptest.NewRequest(http.MethodPost, "/app.name/publish", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "data")
}

func TestHandler_HandlePublish_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/missing/publish", nil)
	handler.ServeHTTP(w, req)

	assert.True(t, w.Code >= 400)
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
	req := httptest.NewRequest(http.MethodPost, "/app.name/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_HandleRollback_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app.name/rollback", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
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
