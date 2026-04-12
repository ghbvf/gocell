package configwrite

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
	mux.HandleFunc("POST /", h.HandleCreate)
	mux.HandleFunc("PUT /{key}", h.HandleUpdate)
	mux.HandleFunc("DELETE /{key}", h.HandleDelete)
	return mux, repo
}

func TestHandler_HandleCreate_OK(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"gocell"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "app.name")
}

func TestHandler_HandleCreate_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleCreate_EmptyKey(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"","value":"v"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleCreate_UnknownField(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"key":"app.name","value":"gocell","extra":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleUpdate_UnknownField(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"value":"new","extra":"y"}`
	req := httptest.NewRequest(http.MethodPut, "/k", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_HandleUpdate_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/k", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
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
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandler_HandleDelete_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/missing", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
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
