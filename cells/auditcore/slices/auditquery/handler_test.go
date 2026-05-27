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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
)

const bootstrapAuditEntryOffset = 2 * time.Hour

// newHandlerMux registers auditquery routes under the canonical API prefix,
// mirroring production wiring so all auth.Mount guards are exercised.
func newHandlerMux(svc *Service) http.Handler {
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/audit", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

// newHandlerStore creates a fresh MemStore for handler tests.
func newHandlerStore(t testing.TB) *ledger.MemStore {
	t.Helper()
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	return store
}

func TestHandleQuery_InvalidTimeFormat(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

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
			req = req.WithContext(auth.TestContext("usr-1", []string{"admin"}))
			mux.ServeHTTP(w, req)

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
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?limit=abc", nil)
	req = req.WithContext(auth.TestContext("usr-1", []string{"admin"}))
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandleQuery_ExceedsMaxLimit(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	// The generated handler routes cursor/limit through httputil.ParsePageParams
	// (PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE F4 absorb): exceeding the 500
	// limit ceiling now produces the canonical ERR_PAGE_SIZE_EXCEEDED envelope
	// shared with every other paginated endpoint.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?limit=501", nil)
	req = req.WithContext(auth.TestContext("usr-1", []string{"admin"}))
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandleQuery_Pagination_FullTraversal(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 7 {
		require.NoError(t, store.Append(context.Background(), &ledger.Entry{
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

	for range 10 {
		url := "/api/v1/audit/entries?limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		// Self-access: subject matches actorId in data.
		req = req.WithContext(auth.TestContext("usr-1", nil))
		mux.ServeHTTP(w, req)

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
			store := newHandlerStore(t)
			svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
			require.NoError(t, err)
			mux := newHandlerMux(svc)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?cursor="+tc.cursor, nil)
			req = req.WithContext(auth.TestContext("usr-1", []string{"admin"}))
			mux.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "ERR_CURSOR_INVALID")
		})
	}
}

// TestAuditEntryResponse_ExcludesInternalFields verifies B2-C-09 boundary:
// internal hash-chain fields (PrevHash, Hash) are excluded from API responses,
// and sensitive payload fields are redacted.
func TestAuditEntryResponse_ExcludesInternalFields(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	// Seed an entry with sensitive payload and internal hash-chain fields.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "ae-1", EventID: "evt-1", EventType: "test.event.v1",
		ActorID: "usr-1", Timestamp: base,
		Payload: []byte(`{"data":"public","password":"secret123"}`),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId=usr-1", nil)
	req = req.WithContext(auth.TestContext("usr-1", nil))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	// Internal hash-chain fields must not appear in the response.
	assert.NotContains(t, body, `"prevHash"`)
	assert.NotContains(t, body, `"hash"`)

	// F23: Sensitive payload fields must be redacted (password → <REDACTED>).
	// json.Marshal HTML-escapes '<' and '>' in string values; the wire body
	// therefore contains the unicode-escape form of the mask.
	assert.NotContains(t, body, "secret123", "sensitive value must not appear in response")
	assert.Contains(t, body, `\u003cREDACTED\u003e`, "redaction mask must appear in payload")
}

// TestAuditEntryResponse_SensitivePayload_Redacted verifies that a payload
// containing a sensitive key ('password') is returned with the value replaced
// by the redaction mask, and the original value is not present.
// F23: HTTP-path redaction coverage.
func TestAuditEntryResponse_SensitivePayload_Redacted(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "ae-pw", EventID: "evt-pw", EventType: "test.redact.v1",
		ActorID: "usr-2", Timestamp: base,
		Payload: []byte(`{"password":"secret123","data":"public"}`),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId=usr-2", nil)
	req = req.WithContext(auth.TestContext("usr-2", nil))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, "secret123", "secret value must be redacted")
	assert.Contains(t, body, `\u003cREDACTED\u003e`, "redaction mask must be present")
}

// TestHandler_RegisterRoutes_AuthzNegative validates that RegisterRoutes installs
// the auditQueryPolicy so unauthenticated and cross-user requests are rejected at
// the route layer, not inside the business handler.
func TestHandler_RegisterRoutes_AuthzNegative(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	h := NewHandler(svc)

	// Seed one entry for usr-1 and one for usr-2.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "rr-1", EventID: "evt-rr-1", EventType: "event.test.v1",
		ActorID: "usr-1", Timestamp: base, Payload: []byte("{}"),
	}))
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "rr-2", EventID: "evt-rr-2", EventType: "event.test.v1",
		ActorID: "usr-2", Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
	}))

	mux := http.NewServeMux()
	require.NoError(t, h.RegisterRoutes(mux))

	tests := []struct {
		name       string
		subject    string
		roles      []string
		actorID    string
		wantStatus int
	}{
		{
			name:       "no_auth",
			subject:    "",
			roles:      nil,
			actorID:    "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "non_admin_cross_user",
			subject:    "usr-1",
			roles:      []string{"viewer"},
			actorID:    "usr-2",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "self_access",
			subject:    "usr-1",
			roles:      nil,
			actorID:    "usr-1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin_cross_user",
			subject:    "admin-1",
			roles:      []string{"admin"},
			actorID:    "usr-2",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/v1/audit/entries"
			if tc.actorID != "" {
				url += "?actorId=" + tc.actorID
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tc.subject != "" {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			mux.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

// TestAuditEntryResponse_PrincipalFields verifies that the 5 new Principal/OccurredAt
// fields (F11) are exposed in the API response when populated in the ledger entry.
func TestAuditEntryResponse_PrincipalFields(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	occurredAt := time.Date(2026, 3, 1, 11, 59, 59, 0, time.UTC)

	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID:            "ae-p1",
		EventID:       "evt-p1",
		EventType:     "user.login.v1",
		ActorID:       "usr-10",
		SubjectID:     "subj-abc",
		TenantID:      "tenant-xyz",
		SessionID:     "sess-001",
		CorrelationID: "corr-999",
		OccurredAt:    occurredAt,
		Timestamp:     base,
		Payload:       []byte(`{}`),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId=usr-10", nil)
	req = req.WithContext(auth.TestContext("usr-10", nil))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	item := resp.Data[0]

	assert.Equal(t, "subj-abc", item["subjectId"])
	assert.Equal(t, "tenant-xyz", item["tenantId"])
	assert.Equal(t, "sess-001", item["sessionId"])
	assert.Equal(t, "corr-999", item["correlationId"])
	assert.Equal(t, occurredAt.UTC().Format(time.RFC3339), item["occurredAt"])
}

// TestAuditEntryResponse_PrincipalFields_OccurredAtZero verifies that occurredAt
// is omitted from the response when the ledger entry has a zero OccurredAt.
func TestAuditEntryResponse_PrincipalFields_OccurredAtZero(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID:        "ae-zero",
		EventID:   "evt-zero",
		EventType: "user.login.v1",
		ActorID:   "usr-11",
		Timestamp: base,
		Payload:   []byte(`{}`),
		// OccurredAt is zero value
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId=usr-11", nil)
	req = req.WithContext(auth.TestContext("usr-11", nil))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, `"occurredAt"`, "zero OccurredAt must be omitted from response")
}

// TestHandleQuery_FilterBySubjectID verifies that the subjectId query parameter
// narrows the result set to entries matching the given SubjectID.
// Note: MemStore.Append overwrites the caller-provided ID with EventID, so we
// identify results by eventId (which equals the store-assigned ID).
func TestHandleQuery_FilterBySubjectID(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		EventID: "evt-subj-A", EventType: "event.v1",
		ActorID: "act-1", SubjectID: "subj-A", Timestamp: base, Payload: []byte(`{}`),
	}))
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		EventID: "evt-subj-B", EventType: "event.v1",
		ActorID: "act-2", SubjectID: "subj-B", Timestamp: base.Add(time.Hour), Payload: []byte(`{}`),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?subjectId=subj-A", nil)
	req = req.WithContext(auth.TestContext("admin-u", []string{"admin"}))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	// MemStore assigns ID = EventID on Append; verify by eventId field
	assert.Equal(t, "evt-subj-A", resp.Data[0]["eventId"])
	assert.Equal(t, "subj-A", resp.Data[0]["subjectId"])
}

// TestHandleQuery_FilterByTenantID verifies that the tenantId query parameter
// narrows the result set to entries matching the given TenantID.
func TestHandleQuery_FilterByTenantID(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		EventID: "evt-tenant-A", EventType: "event.v1",
		ActorID: "act-t1", TenantID: "tenant-A", Timestamp: base, Payload: []byte(`{}`),
	}))
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		EventID: "evt-tenant-B", EventType: "event.v1",
		ActorID: "act-t2", TenantID: "tenant-B", Timestamp: base.Add(time.Hour), Payload: []byte(`{}`),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?tenantId=tenant-B", nil)
	req = req.WithContext(auth.TestContext("admin-u", []string{"admin"}))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "evt-tenant-B", resp.Data[0]["eventId"])
	assert.Equal(t, "tenant-B", resp.Data[0]["tenantId"])
}

// TestHandleQuery_FilterBySessionID verifies that the sessionId query parameter
// narrows the result set to entries matching the given SessionID.
func TestHandleQuery_FilterBySessionID(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		EventID: "evt-sess-X", EventType: "event.v1",
		ActorID: "act-sess1", SessionID: "sess-X", Timestamp: base, Payload: []byte(`{}`),
	}))
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		EventID: "evt-sess-Y", EventType: "event.v1",
		ActorID: "act-sess2", SessionID: "sess-Y", Timestamp: base.Add(time.Hour), Payload: []byte(`{}`),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?sessionId=sess-X", nil)
	req = req.WithContext(auth.TestContext("admin-u", []string{"admin"}))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "evt-sess-X", resp.Data[0]["eventId"])
	assert.Equal(t, "sess-X", resp.Data[0]["sessionId"])
}

// TestHandleQuery_FilterByCorrelationID verifies that the correlationId query
// parameter narrows the result set to entries matching the given CorrelationID.
func TestHandleQuery_FilterByCorrelationID(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		EventID: "evt-corr-100", EventType: "event.v1",
		ActorID: "act-corr1", CorrelationID: "corr-100", Timestamp: base, Payload: []byte(`{}`),
	}))
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		EventID: "evt-corr-200", EventType: "event.v1",
		ActorID: "act-corr2", CorrelationID: "corr-200", Timestamp: base.Add(time.Hour), Payload: []byte(`{}`),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?correlationId=corr-200", nil)
	req = req.WithContext(auth.TestContext("admin-u", []string{"admin"}))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "evt-corr-200", resp.Data[0]["eventId"])
	assert.Equal(t, "corr-200", resp.Data[0]["correlationId"])
}

// TestHandleQuery_CompositeFilter_SubjectAndTenant verifies that combining
// subjectId + tenantId applies AND-conjunction semantics.
func TestHandleQuery_CompositeFilter_SubjectAndTenant(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	// Three entries to exercise AND-conjunction of subjectId + tenantId filters.
	eC1 := &ledger.Entry{ // subj-A + t-X → matches both filters
		EventID: "evt-c1", EventType: "e.v1", ActorID: "a1",
		SubjectID: "subj-A", TenantID: "t-X", Timestamp: base, Payload: []byte(`{}`),
	}
	eC2 := &ledger.Entry{ // subj-A + t-Y → matches subjectId only
		EventID: "evt-c2", EventType: "e.v1", ActorID: "a2",
		SubjectID: "subj-A", TenantID: "t-Y", Timestamp: base.Add(time.Hour), Payload: []byte(`{}`),
	}
	eC3 := &ledger.Entry{ // subj-B + t-X → matches tenantId only
		EventID: "evt-c3", EventType: "e.v1", ActorID: "a3",
		SubjectID: "subj-B", TenantID: "t-X", Timestamp: base.Add(testtime.D2h), Payload: []byte(`{}`),
	}
	entries := []*ledger.Entry{eC1, eC2, eC3}
	for _, e := range entries {
		require.NoError(t, store.Append(context.Background(), e))
	}

	// AND: subjectId=subj-A AND tenantId=t-X → only evt-c1
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?subjectId=subj-A&tenantId=t-X", nil)
	req = req.WithContext(auth.TestContext("admin-u", []string{"admin"}))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "evt-c1", resp.Data[0]["eventId"])
}

// Trust boundary tests (#27q).
func TestHandleQuery_ActorBinding(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	h := NewHandler(svc)

	// securedMux registers the handler via RegisterRoutes, mirroring production
	// wiring so trust boundary tests exercise the same auth.Mount guard.
	securedMux := http.NewServeMux()
	require.NoError(t, h.RegisterRoutes(securedMux))

	// Seed entries for two actors.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "ae-1", EventID: "evt-1", EventType: "event.test.v1",
		ActorID: "usr-1", Timestamp: base, Payload: []byte("{}"),
	}))
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "ae-2", EventID: "evt-2", EventType: "event.test.v1",
		ActorID: "usr-2", Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
	}))
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID:        "ae-3",
		EventID:   "evt-bootstrap-1",
		EventType: "bootstrap.auth.fail",
		ActorID:   "system:bootstrap",
		Timestamp: base.Add(bootstrapAuditEntryOffset),
		Payload:   []byte(`{"reason":"wrong_credentials"}`),
	}))

	tests := []struct {
		name            string
		query           string
		subject         string
		roles           []string
		injectEmptyAuth bool // inject auth.TestContext("", nil) — authenticated but empty Subject
		wantStatus      int
		wantCount       int // -1 = don't check
		wantActorIDs    []string
	}{
		{
			name:       "self actorId matches subject",
			query:      "?actorId=usr-1",
			subject:    "usr-1",
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
		{
			name:       "non-admin no actorId defaults to subject",
			query:      "",
			subject:    "usr-1",
			wantStatus: http.StatusOK,
			wantCount:  1,
		},
		{
			name:         "non-admin eventType without actorId remains self-scoped",
			query:        "?eventType=bootstrap.auth.fail",
			subject:      "usr-1",
			wantStatus:   http.StatusOK,
			wantCount:    0,
			wantActorIDs: []string{},
		},
		{
			name:         "admin eventType without actorId queries globally",
			query:        "?eventType=bootstrap.auth.fail",
			subject:      "admin-user",
			roles:        []string{"admin"},
			wantStatus:   http.StatusOK,
			wantCount:    1,
			wantActorIDs: []string{"system:bootstrap"},
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
		{
			name:            "empty subject in principal returns 401",
			query:           "",
			injectEmptyAuth: true,
			wantStatus:      http.StatusUnauthorized,
			wantCount:       -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries"+tc.query, nil)
			switch {
			case tc.injectEmptyAuth:
				req = req.WithContext(auth.TestContext("", tc.roles))
			case tc.subject != "":
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			securedMux.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCount >= 0 {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				data := resp["data"].([]any)
				assert.Len(t, data, tc.wantCount)
				if tc.wantActorIDs != nil {
					gotActorIDs := make([]string, 0, len(data))
					for _, raw := range data {
						item := raw.(map[string]any)
						gotActorIDs = append(gotActorIDs, item["actorId"].(string))
					}
					assert.Equal(t, tc.wantActorIDs, gotActorIDs)
				}
			}
		})
	}
}
