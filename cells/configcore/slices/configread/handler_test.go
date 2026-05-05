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

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	configget "github.com/ghbvf/gocell/generated/contracts/http/config/get/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

const configBasePath = "/api/v1/config"

// asAdmin attaches an admin Principal to req so it satisfies the
// auth.AnyRole(RoleAdmin) policy applied by RegisterRoutes.
func asAdmin(req *http.Request) *http.Request {
	return req.WithContext(auth.TestContext("admin-user", []string{auth.RoleAdmin}))
}

// setupHandler wires the slice handler onto a celltest mux via RegisterRoutes —
// nested under /api/v1/config to match the production cell_routes.go layout.
func setupHandler() (http.Handler, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository(clock.Real())
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	svc, err := NewService(repo, codec, slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	mux := celltest.NewTestMux()
	mux.Route(configBasePath, func(sub kcell.RouteMux) {
		if err := NewHandler(svc).RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
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
		require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
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
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
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
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
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
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Sensitive: false,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
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
