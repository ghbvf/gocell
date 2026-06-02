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
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
)

const bootstrapAuditEntryOffset = 2 * time.Hour

// seedThirdEntryOffset spaces the third seeded entry in subjectId-filter tests
// (TEST-TIME-LITERAL-01: durations live in package-level consts, not literals).
const seedThirdEntryOffset = 2 * time.Hour

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

// TestHandler_RegisterRoutes_TenantBearing_FailClosed proves the audit query
// endpoint fails closed for any tenant-bearing caller (#1339 F2). Until
// tenant-scoped audit filtering lands (epic #1337 PR-2: AuditFilters.TenantID +
// tenant-scoped WHERE), a request whose principal carries a non-empty TenantID
// must be REJECTED (403) rather than silently served un-isolated, cross-tenant
// results. Denial is independent of admin role / actorId — fail-closed for
// everyone tenant-bearing.
func TestHandler_RegisterRoutes_TenantBearing_FailClosed(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	h := NewHandler(svc)

	mux := http.NewServeMux()
	require.NoError(t, h.RegisterRoutes(mux))

	const tenantUUID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	for _, tc := range []struct {
		name  string
		roles []string
	}{
		{"non_admin_tenant_bearing", []string{"viewer"}},
		{"admin_tenant_bearing", []string{"admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &auth.Principal{
				Kind:       auth.PrincipalUser,
				Subject:    "usr-1",
				Roles:      tc.roles,
				TenantID:   tenantUUID,
				AuthMethod: "test",
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil).
				WithContext(auth.WithPrincipal(context.Background(), p))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			assert.Equal(t, http.StatusForbidden, w.Code,
				"tenant-bearing audit query must fail closed until PR-2 tenant filtering lands")
		})
	}
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

	tests := []actorBindingCase{
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
			// subjectId must not let a non-admin smuggle a cross-user actorId
			// past the policy: actorId=usr-2 still trips the 403 (#1290 authz).
			name:       "non-admin subjectId with cross-user actorId returns 403",
			query:      "?subjectId=victim&actorId=usr-2",
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
			assertActorBindingCase(t, securedMux, tc)
		})
	}
}

// TestHandleQuery_SubjectFilter pins #1290 (F23): an admin can filter audit
// entries by subjectId to investigate impersonation (actorId != subjectId). The
// subjectId query parameter binds to AuditFilters.SubjectID and narrows the SQL
// WHERE to subject_id = ?. Without the binding every row is returned (RED).
func TestHandleQuery_SubjectFilter(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := []*ledger.Entry{
		{
			ID: "se-1", EventID: "evt-s1", EventType: "event.test.v1",
			ActorID: "actor-1", SubjectID: "alice", Timestamp: base, Payload: []byte("{}"),
		},
		{
			ID: "se-2", EventID: "evt-s2", EventType: "event.test.v1",
			ActorID: "actor-2", SubjectID: "bob", Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
		},
		{
			ID: "se-3", EventID: "evt-s3", EventType: "event.test.v1",
			ActorID: "actor-3", SubjectID: "alice", Timestamp: base.Add(seedThirdEntryOffset), Payload: []byte("{}"),
		},
	}
	for _, e := range seed {
		require.NoError(t, store.Append(context.Background(), e))
	}

	// Admin (global, no actorId) filters by subjectId=alice → only the two
	// alice-subject rows (se-1, se-3), regardless of differing actors.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?subjectId=alice", nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []struct {
			SubjectID string `json:"subjectId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data, 2)
	for _, d := range resp.Data {
		assert.Equal(t, "alice", d.SubjectID)
	}
}

// TestHandleQuery_SubjectFilter_NonAdminScopedToActorSelf proves the subjectId
// filter composes with — and never escapes — the actor-self scoping that the
// auditQueryPolicy already enforces for non-admin callers. A non-admin's query
// is always AND-ed with actor_id = self, so subjectId only narrows within the
// caller's own actions and cannot leak another user's rows (#1290 authz design).
func TestHandleQuery_SubjectFilter_NonAdminScopedToActorSelf(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := []*ledger.Entry{
		// usr-1's own action targeting subject "victim".
		{
			ID: "ns-1", EventID: "evt-n1", EventType: "event.test.v1",
			ActorID: "usr-1", SubjectID: "victim", Timestamp: base, Payload: []byte("{}"),
		},
		// usr-1's own action targeting subject "other" — must be excluded by the
		// subjectId=victim filter (narrowing within actor-self).
		{
			ID: "ns-2", EventID: "evt-n2", EventType: "event.test.v1",
			ActorID: "usr-1", SubjectID: "other", Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
		},
		// usr-2's action targeting subject "victim" — must never be visible to
		// usr-1 (actor-self scoping; cross-user safety).
		{
			ID: "ns-3", EventID: "evt-n3", EventType: "event.test.v1",
			ActorID: "usr-2", SubjectID: "victim", Timestamp: base.Add(seedThirdEntryOffset), Payload: []byte("{}"),
		},
	}
	for _, e := range seed {
		require.NoError(t, store.Append(context.Background(), e))
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?subjectId=victim", nil)
	req = req.WithContext(auth.TestContext("usr-1", nil)) // non-admin
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []struct {
			EventID   string `json:"eventId"`
			ActorID   string `json:"actorId"`
			SubjectID string `json:"subjectId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// Only the ns-1 row (eventId evt-n1): actor=usr-1 AND subject=victim. ns-2 is
	// excluded by the subjectId filter, ns-3 by actor-self scoping.
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "evt-n1", resp.Data[0].EventID)
	assert.Equal(t, "usr-1", resp.Data[0].ActorID)
	assert.Equal(t, "victim", resp.Data[0].SubjectID)
}

// TestHandleQuery_TraceIDFilter_Admin verifies that ?traceId=X returns only
// entries whose TraceID == X for an admin caller (global scope).
// Without the handler binding TraceID → AuditFilters.TraceID the filter has no
// effect and all three entries are returned (RED). The test also confirms that
// the matched entry's traceId is surfaced in the response item.
func TestHandleQuery_TraceIDFilter_Admin(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seed := []*ledger.Entry{
		{
			ID: "tid-1", EventID: "evt-tid-1", EventType: "event.test.v1",
			ActorID: "actor-a", TraceID: "trace-abc",
			Timestamp: base, Payload: []byte("{}"),
		},
		{
			ID: "tid-2", EventID: "evt-tid-2", EventType: "event.test.v1",
			ActorID: "actor-b", TraceID: "trace-abc",
			Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
		},
		{
			ID: "tid-3", EventID: "evt-tid-3", EventType: "event.test.v1",
			ActorID: "actor-c", TraceID: "trace-xyz",
			Timestamp: base.Add(seedThirdEntryOffset), Payload: []byte("{}"),
		},
	}
	for _, e := range seed {
		require.NoError(t, store.Append(context.Background(), e))
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?traceId=trace-abc", nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []struct {
			EventID string `json:"eventId"`
			TraceID string `json:"traceId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2, "expected exactly two entries with traceId=trace-abc")
	for _, d := range resp.Data {
		assert.Equal(t, "trace-abc", d.TraceID, "response item must surface traceId")
	}
	// Confirm the trace-xyz entry is not returned.
	for _, d := range resp.Data {
		assert.NotEqual(t, "evt-tid-3", d.EventID, "trace-xyz entry must not be returned")
	}
}

// TestHandleQuery_TraceIDFilter_EmptyParam verifies that an empty ?traceId=
// query parameter is treated as NO filter (equivalent to omitting the
// parameter entirely) and returns all matching rows, not WHERE trace_id="".
func TestHandleQuery_TraceIDFilter_EmptyParam(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seed := []*ledger.Entry{
		{
			ID: "ep-1", EventID: "evt-ep-1", EventType: "event.test.v1",
			ActorID: "usr-ep", TraceID: "trace-present",
			Timestamp: base, Payload: []byte("{}"),
		},
		{
			ID: "ep-2", EventID: "evt-ep-2", EventType: "event.test.v1",
			ActorID: "usr-ep", TraceID: "",
			Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
		},
	}
	for _, e := range seed {
		require.NoError(t, store.Append(context.Background(), e))
	}

	// ?traceId= (empty value) must behave as no filter → all rows for the actor.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId=usr-ep&traceId=", nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []struct {
			EventID string `json:"eventId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data, 2, "empty ?traceId= must act as no filter and return all rows")
}

// TestHandleQuery_TraceIDFilter_NonAdminScopedToActorSelf verifies that a
// non-admin caller with ?traceId=X still only sees their own actor_id rows.
// The traceId filter is AND-ed with the actor-self policy enforced by
// auditQueryPolicy — it does NOT widen scope beyond the caller's own actions.
func TestHandleQuery_TraceIDFilter_NonAdminScopedToActorSelf(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	seed := []*ledger.Entry{
		// usr-1's own action under trace-abc — must be visible.
		{
			ID: "ta-1", EventID: "evt-ta-1", EventType: "event.test.v1",
			ActorID: "usr-1", TraceID: "trace-abc",
			Timestamp: base, Payload: []byte("{}"),
		},
		// usr-2's action under the same trace-abc — must NOT be visible to usr-1.
		{
			ID: "ta-2", EventID: "evt-ta-2", EventType: "event.test.v1",
			ActorID: "usr-2", TraceID: "trace-abc",
			Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
		},
		// usr-1's action under a different trace — excluded by the traceId filter.
		{
			ID: "ta-3", EventID: "evt-ta-3", EventType: "event.test.v1",
			ActorID: "usr-1", TraceID: "trace-xyz",
			Timestamp: base.Add(seedThirdEntryOffset), Payload: []byte("{}"),
		},
	}
	for _, e := range seed {
		require.NoError(t, store.Append(context.Background(), e))
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?traceId=trace-abc", nil)
	req = req.WithContext(auth.TestContext("usr-1", nil)) // non-admin
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []struct {
			EventID string `json:"eventId"`
			ActorID string `json:"actorId"`
			TraceID string `json:"traceId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// Only ta-1 (usr-1 + trace-abc). ta-2 excluded by actor-self; ta-3 by traceId.
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "evt-ta-1", resp.Data[0].EventID)
	assert.Equal(t, "usr-1", resp.Data[0].ActorID)
	assert.Equal(t, "trace-abc", resp.Data[0].TraceID)
}

type actorBindingCase struct {
	name            string
	query           string
	subject         string
	roles           []string
	injectEmptyAuth bool
	wantStatus      int
	wantCount       int
	wantActorIDs    []string
}

func assertActorBindingCase(t *testing.T, mux *http.ServeMux, tc actorBindingCase) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries"+tc.query, nil)
	switch {
	case tc.injectEmptyAuth:
		req = req.WithContext(auth.TestContext("", tc.roles))
	case tc.subject != "":
		req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
	}
	mux.ServeHTTP(w, req)

	assert.Equal(t, tc.wantStatus, w.Code)
	if tc.wantCount < 0 {
		return
	}
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].([]any)
	assert.Len(t, data, tc.wantCount)
	if tc.wantActorIDs == nil {
		return
	}
	gotActorIDs := make([]string, 0, len(data))
	for _, raw := range data {
		item := raw.(map[string]any)
		gotActorIDs = append(gotActorIDs, item["actorId"].(string))
	}
	assert.Equal(t, tc.wantActorIDs, gotActorIDs)
}
