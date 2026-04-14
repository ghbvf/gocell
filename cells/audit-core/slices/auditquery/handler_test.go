package auditquery

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authCtx creates a context with the given subject and roles for auth testing.
func authCtx(subject string, roles []string) context.Context {
	ctx := ctxkeys.WithSubject(context.Background(), subject)
	return auth.WithClaims(ctx, auth.Claims{Subject: subject, Roles: roles})
}

func TestHandleQuery_InvalidTimeFormat(t *testing.T) {
	repo := mem.NewAuditRepository()
	svc := NewService(repo, testCodec(), slog.Default())
	h := NewHandler(svc)

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid from parameter",
			query:      "?from=not-a-date",
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_INVALID_TIME_FORMAT",
		},
		{
			name:       "invalid to parameter",
			query:      "?to=yesterday",
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_INVALID_TIME_FORMAT",
		},
		{
			name:       "valid RFC3339 from",
			query:      "?from=2024-01-01T00:00:00Z",
			wantStatus: http.StatusOK,
		},
		{
			name:       "no time params",
			query:      "",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries"+tc.query, nil)
			// Inject auth context so the handler doesn't reject with 401.
			req = req.WithContext(authCtx("usr-1", []string{"admin"}))
			h.HandleQuery(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCode != "" {
				var resp struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tc.wantCode, resp.Error.Code)
			}
		})
	}
}

func TestHandleQuery_InvalidLimit(t *testing.T) {
	repo := mem.NewAuditRepository()
	svc := NewService(repo, testCodec(), slog.Default())
	h := NewHandler(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?limit=abc", nil)
	req = req.WithContext(authCtx("usr-1", []string{"admin"}))
	h.HandleQuery(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandleQuery_ExceedsMaxLimit(t *testing.T) {
	repo := mem.NewAuditRepository()
	svc := NewService(repo, testCodec(), slog.Default())
	h := NewHandler(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?limit=501", nil)
	req = req.WithContext(authCtx("usr-1", []string{"admin"}))
	h.HandleQuery(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandleQuery_Pagination_FullTraversal(t *testing.T) {
	repo := mem.NewAuditRepository()
	svc := NewService(repo, testCodec(), slog.Default())
	h := NewHandler(svc)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		require.NoError(t, repo.Append(context.Background(), &domain.AuditEntry{
			ID:        fmt.Sprintf("ae-%02d", i),
			EventID:   fmt.Sprintf("evt-%02d", i),
			EventType: "event.test.v1",
			ActorID:   "usr-1",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
			Payload:   []byte("{}"),
		}))
	}

	var allIDs []string
	cursor := ""

	for page := 0; page < 10; page++ {
		url := "/api/v1/audit/entries?limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		// Self-access: subject matches actorId in data.
		req = req.WithContext(authCtx("usr-1", nil))
		h.HandleQuery(w, req)

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

func TestHandleQuery_InvalidCursor(t *testing.T) {
	codec := testCodec()

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
			repo := mem.NewAuditRepository()
			svc := NewService(repo, testCodec(), slog.Default())
			h := NewHandler(svc)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?cursor="+tc.cursor, nil)
			req = req.WithContext(authCtx("usr-1", []string{"admin"}))
			h.HandleQuery(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "ERR_CURSOR_INVALID")
		})
	}
}

func TestAuditEntryResponse_ExcludesInternalFields(t *testing.T) {
	resp := toAuditEntryResponse(&domain.AuditEntry{
		ID: "ae-1", EventID: "evt-1", EventType: "test.event.v1",
		ActorID: "usr-1", Timestamp: time.Now(),
		Payload:  []byte(`{"secret":"data"}`),
		PrevHash: "abc123", Hash: "def456",
	})
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	body := string(b)

	assert.Contains(t, body, `"id"`)
	assert.Contains(t, body, `"eventId"`)
	assert.Contains(t, body, `"eventType"`)
	assert.Contains(t, body, `"actorId"`)
	assert.Contains(t, body, `"timestamp"`)

	assert.Contains(t, body, `"payload"`)

	assert.NotContains(t, body, `"prevHash"`)
	assert.NotContains(t, body, `"hash"`)
}

// Trust boundary tests (#27q)
func TestHandleQuery_ActorBinding(t *testing.T) {
	repo := mem.NewAuditRepository()
	svc := NewService(repo, testCodec(), slog.Default())
	h := NewHandler(svc)

	// Seed entries for two actors
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Append(context.Background(), &domain.AuditEntry{
		ID: "ae-1", EventID: "evt-1", EventType: "event.test.v1",
		ActorID: "usr-1", Timestamp: base, Payload: []byte("{}"),
	}))
	require.NoError(t, repo.Append(context.Background(), &domain.AuditEntry{
		ID: "ae-2", EventID: "evt-2", EventType: "event.test.v1",
		ActorID: "usr-2", Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
	}))

	tests := []struct {
		name       string
		query      string
		subject    string
		roles      []string
		wantStatus int
		wantCount  int // -1 = don't check
	}{
		{
			name:       "self actorId matches subject",
			query:      "?actorId=usr-1",
			subject:    "usr-1",
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
		{
			name:       "no actorId defaults to subject",
			query:      "",
			subject:    "usr-1",
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
		{
			name:       "other actorId without admin returns 403",
			query:      "?actorId=usr-2",
			subject:    "usr-1",
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
			wantCount:  -1,
		},
		{
			name:       "other actorId with admin allowed",
			query:      "?actorId=usr-2",
			subject:    "admin-user",
			roles:      []string{"admin"},
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
		{
			name:       "no subject returns 401",
			query:      "",
			subject:    "",
			wantStatus: http.StatusUnauthorized,
			wantCount:  -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries"+tc.query, nil)
			if tc.subject != "" {
				req = req.WithContext(authCtx(tc.subject, tc.roles))
			}
			h.HandleQuery(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCount >= 0 {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				data := resp["data"].([]any)
				assert.Len(t, data, tc.wantCount)
			}
		})
	}
}
