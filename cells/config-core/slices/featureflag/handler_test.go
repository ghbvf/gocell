package featureflag

import (
	"bytes"
	"context"
	"encoding/json"
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

var flagHandlerTestKey = bytes.Repeat([]byte("f"), 32)

func TestToFeatureFlagResponse_NilInput(t *testing.T) {
	var got FeatureFlagResponse
	assert.NotPanics(t, func() { got = toFeatureFlagResponse(nil) })
	assert.Zero(t, got.ID)
}

func TestToEvaluateResultResponse_NilInput(t *testing.T) {
	var got EvaluateResultResponse
	assert.NotPanics(t, func() { got = toEvaluateResultResponse(nil) })
	assert.Zero(t, got.Key)
}

func TestFeatureFlagResponse_Fields(t *testing.T) {
	flag := &domain.FeatureFlag{
		ID: "ff-1", Key: "dark-mode", Type: domain.FlagBoolean,
		Enabled: true, RolloutPercentage: 80,
	}
	resp := toFeatureFlagResponse(flag)

	assert.Equal(t, "ff-1", resp.ID)
	assert.Equal(t, "dark-mode", resp.Key)
	assert.Equal(t, "boolean", resp.Type)
	assert.True(t, resp.Enabled)
	assert.Equal(t, 80, resp.RolloutPercentage)

	// Verify camelCase JSON keys.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"id"`)
	assert.Contains(t, s, `"key"`)
	assert.Contains(t, s, `"type"`)
	assert.Contains(t, s, `"enabled"`)
	assert.Contains(t, s, `"rolloutPercentage"`)
}

func TestEvaluateResultResponse_Fields(t *testing.T) {
	result := &EvaluateResult{Key: "dark-mode", Enabled: true}
	resp := toEvaluateResultResponse(result)

	assert.Equal(t, "dark-mode", resp.Key)
	assert.True(t, resp.Enabled)

	// Verify camelCase JSON keys.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"key"`)
	assert.Contains(t, s, `"enabled"`)
}

func setupHandler() (http.Handler, *mem.FlagRepository) {
	h, r, _ := setupHandlerWithCodec()
	return h, r
}

func setupHandlerWithCodec() (http.Handler, *mem.FlagRepository, *query.CursorCodec) {
	repo := mem.NewFlagRepository()
	codec, _ := query.NewCursorCodec(flagHandlerTestKey)
	svc := NewService(repo, codec, slog.Default(), query.RunModeProd)
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.HandleList)
	mux.HandleFunc("GET /{key}", h.HandleGet)
	mux.HandleFunc("POST /{key}/evaluate", h.HandleEvaluate)
	return mux, repo, codec
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

	// Verify camelCase JSON keys (#27n).
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	var dataMap map[string]any
	require.NoError(t, json.Unmarshal(raw["data"], &dataMap))
	assert.Contains(t, dataMap, "id", "key must be camelCase")
	assert.Contains(t, dataMap, "key", "key must be camelCase")
	assert.Contains(t, dataMap, "type", "key must be camelCase")
	assert.Contains(t, dataMap, "enabled", "key must be camelCase")
	assert.Contains(t, dataMap, "rolloutPercentage", "key must be camelCase")
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

	// Verify camelCase JSON keys (#27n).
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	var dataMap map[string]any
	require.NoError(t, json.Unmarshal(raw["data"], &dataMap))
	assert.Contains(t, dataMap, "key", "key must be camelCase")
	assert.Contains(t, dataMap, "enabled", "key must be camelCase")
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

func TestHandler_HandleList_InvalidLimit(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?limit=abc", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandler_HandleList_ExceedsMaxLimit(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?limit=501", nil)
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandler_HandleList_Pagination_FullTraversal(t *testing.T) {
	handler, repo := setupHandler()
	keys := []string{"flag-a", "flag-b", "flag-c", "flag-d", "flag-e", "flag-f", "flag-g"}
	for _, k := range keys {
		require.NoError(t, repo.Create(context.Background(), &domain.FeatureFlag{
			ID: "ff-" + k, Key: k, Type: domain.FlagBoolean, Enabled: true,
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
	codec, _ := query.NewCursorCodec(flagHandlerTestKey)

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
			req := httptest.NewRequest(http.MethodGet, "/?cursor="+tc.cursor, nil)
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "ERR_CURSOR_INVALID")
		})
	}
}
