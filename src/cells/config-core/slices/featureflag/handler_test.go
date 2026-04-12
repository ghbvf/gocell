package featureflag

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupHandler() (http.Handler, *mem.FlagRepository) {
	repo := mem.NewFlagRepository()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	codec, _ := query.NewCursorCodec(key)
	svc := NewService(repo, codec, slog.Default())
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.HandleList)
	mux.HandleFunc("GET /{key}", h.HandleGet)
	mux.HandleFunc("POST /{key}/evaluate", h.HandleEvaluate)
	return mux, repo
}

func TestHandler_HandleList(t *testing.T) {
	handler, repo := setupHandler()
	require.NoError(t, repo.Create(context.Background(), &domain.FeatureFlag{
		ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: true,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "dark-mode")
	assert.Contains(t, w.Body.String(), "\"hasMore\"")
}

func TestHandler_HandleGet_Found(t *testing.T) {
	handler, repo := setupHandler()
	require.NoError(t, repo.Create(context.Background(), &domain.FeatureFlag{
		ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: true,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dark-mode", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "dark-mode")
}

func TestHandler_HandleGet_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_HandleEvaluate_OK(t *testing.T) {
	handler, repo := setupHandler()
	require.NoError(t, repo.Create(context.Background(), &domain.FeatureFlag{
		ID: "f1", Key: "dark-mode", Type: domain.FlagBoolean, Enabled: true,
	}))

	w := httptest.NewRecorder()
	body := `{"subject":"user-1"}`
	req := httptest.NewRequest(http.MethodPost, "/dark-mode/evaluate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "dark-mode")
}

func TestHandler_HandleEvaluate_UnknownField(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"subject":"user-1","extra":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/dark-mode/evaluate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleEvaluate_BadJSON(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dark-mode/evaluate", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_HandleEvaluate_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	body := `{"subject":"user-1"}`
	req := httptest.NewRequest(http.MethodPost, "/missing/evaluate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	// Service returns ErrFlagNotFound -> 404.
	assert.Equal(t, http.StatusNotFound, w.Code)
}
