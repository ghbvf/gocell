package configread

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupHandler() (http.Handler, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	svc := NewService(repo, codec, slog.Default())
	mux := http.NewServeMux()
	h := NewHandler(svc)
	mux.HandleFunc("GET /{key}", h.HandleGet)
	mux.HandleFunc("GET /", h.HandleList)
	return mux, repo
}

func TestHandler_HandleGet_Found(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app.name", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "gocell")
}

func TestHandler_HandleGet_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing-key", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_HandleList_OK(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-1", Key: "k1", Value: "v1", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-2", Key: "k2", Value: "v2", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"data\":")
	assert.Contains(t, w.Body.String(), "\"hasMore\":")
}

func TestHandler_HandleList_Empty(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"data\":")
	assert.Contains(t, w.Body.String(), "\"hasMore\":false")
}

func TestHandler_HandleList_Pagination_FullTraversal(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	keys := []string{"key-a", "key-b", "key-c", "key-d", "key-e", "key-f", "key-g"}
	for i, k := range keys {
		require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
			ID: "cfg-" + k, Key: k, Value: "v" + k, Version: 1,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}))
	}

	var allIDs []string
	cursor := ""

	for page := 0; page < 10; page++ {
		url := "/?limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		handler.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		data := resp["data"].([]any)
		for _, item := range data {
			m := item.(map[string]any)
			allIDs = append(allIDs, m["ID"].(string))
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
