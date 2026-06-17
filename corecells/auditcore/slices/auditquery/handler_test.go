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

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	auditget "github.com/ghbvf/gocell/generated/contracts/http/audit/get/v1"
	auditlist "github.com/ghbvf/gocell/generated/contracts/http/audit/list/v1"
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

// testCaptureHandler is a minimal slog.Handler that accumulates every log record.
// We MUST NOT use slog.NewJSONHandler or slog.NewTextHandler here — both are
// banned by archtest SLOG-HANDLER-SEALED-FUNNEL-01 outside the logging package.
type testCaptureHandler struct {
	records []slog.Record
}

func (h *testCaptureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *testCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *testCaptureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *testCaptureHandler) WithGroup(_ string) slog.Handler      { return h }

type auditVisibilityCase struct {
	name          string
	subject       string
	roles         []string
	tenantID      string
	wantCount     int
	wantSystemRow bool // #1618 F7: a tenant-less system row appears, marked scope="system"
}

// auditQueryTestTenant is a canonical tenant UUID for handler tests. auditquery
// fail-closes on an empty principal tenant (epic #1337 PR-2a, F1), so every
// handler test that expects to reach the query path must carry a tenant.
const auditQueryTestTenant = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

// auditQueryTestTenantB is a second canonical tenant UUID used in the PR-5
// (#1343) cross-tenant visibility matrix test. A super-admin must see rows from
// both tenants; a regular admin must NOT see rows from another tenant.
const auditQueryTestTenantB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"

// auditTestCtx builds a production-faithful handler-test context: a tenant-bearing
// principal PLUS an allow-all Authorizer, mirroring production where the primary
// listener always injects a PDP. Since F1 made an empty actorId a permissioned
// ledger read (no longer an implicit self-read), happy-path tests need an
// Authorizer in context to pass auditQueryPolicy and reach the handler; this
// helper supplies one. Tests asserting the fail-closed (no-PDP) path use
// auditTestCtxNoAuthz; deny tests wrap with withDenyAuthorizer (which shadows the
// allow). auth.TestContext alone leaves TenantID empty, which the F1 isolation
// guard rejects; the empty-tenant rejection is covered by TestList_EmptyTenant_Forbidden.
func auditTestCtx(subject string, roles []string) context.Context {
	return withAllowAuthorizer(auditTestCtxNoAuthz(subject, roles))
}

// auditTestCtxNoAuthz builds a tenant-bearing principal context WITHOUT an
// Authorizer — used by tests that exercise the fail-closed path (no PDP wired →
// auditQueryPolicy denies any non-self read) and by table tests that inject a
// per-case Authorizer via withAuthzCtx.
func auditTestCtxNoAuthz(subject string, roles []string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    subject,
		Roles:      append([]string(nil), roles...),
		TenantID:   auditQueryTestTenant,
		AuthMethod: "test",
	})
}

// mockAuthorizer is a test-only implementation of auth.Authorizer that returns
// a fixed Decision for every Authorize call. Place it in the same-package test
// file per GoCell's mock placement rule (go-standards.md §Naming).
type mockAuthorizer struct {
	decision authz.Decision
	err      error
}

func (m *mockAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return m.decision, m.err
}

// allowAuthorizer returns a mockAuthorizer that always grants permission.
func allowAuthorizer() *mockAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("allowAuthorizer: authz.Allow: " + err.Error())
	}
	return &mockAuthorizer{decision: dec}
}

// denyAuthorizer returns a mockAuthorizer that always denies permission.
func denyAuthorizer(reason string) *mockAuthorizer {
	return &mockAuthorizer{decision: authz.Deny(reason)}
}

// withAllowAuthorizer returns a context carrying an allow-all Authorizer.
func withAllowAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, allowAuthorizer())
}

// withDenyAuthorizer returns a context carrying a deny-all Authorizer.
func withDenyAuthorizer(ctx context.Context, reason string) context.Context {
	return auth.WithAuthorizer(ctx, denyAuthorizer(reason))
}

func TestHandleQuery_InvalidTimeFormat(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
			req = req.WithContext(auditTestCtx("usr-1", []string{"admin"}))
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

func assertAuditVisibilityCase(t *testing.T, mux http.Handler, tc auditVisibilityCase) {
	t.Helper()

	p := &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    tc.subject,
		Roles:      tc.roles,
		TenantID:   tc.tenantID,
		AuthMethod: "test",
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	// Allow-all Authorizer: this matrix exercises data-layer RowScope visibility,
	// which is reached only after the empty-actorId read passes the PDP gate (F1).
	req = req.WithContext(withAllowAuthorizer(auth.WithPrincipal(context.Background(), p)))
	mux.ServeHTTP(w, req)

	if isSuperAdmin(tc.roles) {
		// Super-admin with no CrossTenantQueryStore wired: graceful-absent path per
		// ADR #1810 returns 501 (RowScopeAllUnsupportedError). The handler routes to
		// CrossTenantQueryStore when wired (200 + cross-tenant rows, see
		// TestHandleQuery_SuperAdmin_CrossTenantStore_200); this matrix uses the
		// base service (no WithCrossTenantStore) to exercise the graceful-absent
		// branch specifically. FR-007 audit is still emitted (asserted by caller).
		require.Equal(t, http.StatusNotImplemented, w.Code,
			"tc=%s: super-admin without CrossTenantQueryStore must return 501 (graceful-absent), body=%s", tc.name, w.Body.String())
		return
	}

	require.Equal(t, http.StatusOK, w.Code, "tc=%s body=%s", tc.name, w.Body.String())
	var resp struct {
		Data []struct {
			Scope string `json:"scope"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "tc=%s", tc.name)
	assert.Len(t, resp.Data, tc.wantCount, "tc=%s: wrong row count", tc.name)
	assertAuditRowScopes(t, resp.Data, tc)
}

func assertAuditRowScopes(t *testing.T, rows []struct {
	Scope string `json:"scope"`
}, tc auditVisibilityCase,
) {
	t.Helper()

	// #1618 F7: every returned row carries a scope marker; the admin's
	// tenant-wide read surfaces the tenant-less system row marked
	// "system", own-tenant rows "tenant".
	sawSystem := false
	for _, row := range rows {
		assert.Contains(t, []string{"tenant", "system"}, row.Scope,
			"tc=%s: row scope must be tenant|system, got %q", tc.name, row.Scope)
		if row.Scope == "system" {
			sawSystem = true
		}
	}
	assert.Equal(t, tc.wantSystemRow, sawSystem,
		"tc=%s: system-row scope visibility mismatch", tc.name)
}

func countAuditMandatoryRecords(records []slog.Record) int {
	errorCount := 0
	for _, rec := range records {
		if rec.Level != slog.LevelError {
			continue
		}
		if auditRecordHasMandatoryKeys(rec) {
			errorCount++
		}
	}
	return errorCount
}

func auditRecordHasMandatoryKeys(rec slog.Record) bool {
	hasActor, hasScope, hasTenant, hasReason := false, false, false, false
	rec.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "actor":
			hasActor = true
		case "scope":
			hasScope = true
		case "tenant":
			hasTenant = true
		case "reason":
			hasReason = true
		}
		return true
	})
	return hasActor && hasScope && hasTenant && hasReason
}

func isSuperAdmin(roles []string) bool {
	for _, r := range roles {
		if r == auth.RoleSuperAdmin {
			return true
		}
	}
	return false
}

func TestHandleQuery_InvalidLimit(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?limit=abc", nil)
	req = req.WithContext(auditTestCtx("usr-1", []string{"admin"}))
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandleQuery_ExceedsMaxLimit(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	// The generated handler routes cursor/limit through httputil.ParsePageParams
	// (PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE F4 absorb): exceeding the 500
	// limit ceiling now produces the canonical ERR_PAGE_SIZE_EXCEEDED envelope
	// shared with every other paginated endpoint.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?limit=501", nil)
	req = req.WithContext(auditTestCtx("usr-1", []string{"admin"}))
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandleQuery_Pagination_FullTraversal(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
		req = req.WithContext(auditTestCtx("usr-1", nil))
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
			svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
			require.NoError(t, err)
			mux := newHandlerMux(svc)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?cursor="+tc.cursor, nil)
			req = req.WithContext(auditTestCtx("usr-1", []string{"admin"}))
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
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
	req = req.WithContext(auditTestCtx("usr-1", nil))
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
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
	req = req.WithContext(auditTestCtx("usr-2", nil))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, "secret123", "secret value must be redacted")
	assert.Contains(t, body, `\u003cREDACTED\u003e`, "redaction mask must be present")
}

// TestHandler_RegisterRoutes_AuthzNegative validates that RegisterRoutes installs
// the auditQueryPolicy so unauthenticated and cross-user requests are rejected at
// the route layer, not inside the business handler.
// TestList_EmptyTenant_Forbidden (epic #1337 PR-2a, F1): an authenticated
// principal with no tenant must be rejected with 403 rather than degrade to the
// store's system-chain read (empty tenant → tenant_id = ” rows only, never the
// caller's intended tenant data) — the fail-open vector the second-round review
// flagged P0.
func TestList_EmptyTenant_Forbidden(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	// auth.TestContext leaves TenantID empty — the exact fail-open vector. Wrap an
	// allow-all Authorizer so the empty-actorId read passes the PDP gate (F1) and
	// the 403 under test is the tenant-isolation fail-close inside List, not a
	// no-PDP gate rejection.
	req = req.WithContext(withAllowAuthorizer(auth.TestContext("usr-1", []string{"admin"})))
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code,
		"empty-tenant principal must be 403 (tenant isolation fail-closed); body=%s", w.Body.String())
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ERR_AUTH_FORBIDDEN", resp.Error.Code)
}

// TestList_NonCanonicalTenant_InternalError (#1618, Fix 7a): a principal whose
// TenantID is non-empty but not a valid canonical UUID (e.g. "not-a-uuid") must
// cause the handler to return 500 (ErrInternal), not 403 or 400.
//
// Rationale: the JWT authenticator canonicalises tenant_id claims (malformed →
// 401 at the edge), so a non-canonical string reaching this point is a
// server-side invariant break — tenant.ParseTenantID failure maps to
// errcode.KindInternal/ErrInternal, which the generated handler renders as 500.
func TestList_NonCanonicalTenant_InternalError(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	// Inject a principal whose TenantID is syntactically non-empty but is not a
	// canonical UUID — this bypasses the empty-tenant 403 gate and reaches the
	// ParseTenantID call, which must return an error mapped to 500. An allow-all
	// Authorizer lets the empty-actorId read pass the PDP gate (F1) so the failure
	// under test is the ParseTenantID 500, not a no-PDP gate rejection.
	req = req.WithContext(withAllowAuthorizer(auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    "usr-1",
		Roles:      []string{"admin"},
		TenantID:   "not-a-uuid",
		AuthMethod: "test",
	})))
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code,
		"non-canonical tenant UUID must yield 500 (invariant break); body=%s", w.Body.String())
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ERR_INTERNAL", resp.Error.Code,
		"error code must be ERR_INTERNAL for a principal with malformed tenant UUID")
}

func TestHandler_RegisterRoutes_AuthzNegative(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
		name         string
		subject      string
		roles        []string
		actorID      string
		withAuthzCtx func(context.Context) context.Context // optional Authorizer injection
		wantStatus   int
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
			// F1: an empty actorId is NO LONGER an implicit self-read — it is a
			// permissioned ledger read. With no Authorizer wired it fails closed
			// (403), same as any other non-self read. A non-admin reads its own
			// rows by naming itself (actorId=subject), covered by self_access.
			name:       "empty_actorId_requires_permission_failclosed",
			subject:    "usr-1",
			roles:      nil,
			actorID:    "",
			wantStatus: http.StatusForbidden,
		},
		{
			// The "other actors" branch now delegates to the PDP via
			// RequirePermission(PermAuditRead). Callers MUST inject an Authorizer
			// via auth.WithAuthorizer; without one the policy fails closed → 403.
			name:       "admin_cross_user_missing_authorizer_failclosed",
			subject:    "admin-1",
			roles:      []string{"admin"},
			actorID:    "usr-2",
			wantStatus: http.StatusForbidden, // fail-closed: no Authorizer in ctx
		},
		{
			// Admin cross-user query with an ALLOW Authorizer in context → 200.
			name:         "admin_cross_user_allow",
			subject:      "admin-1",
			roles:        []string{"admin"},
			actorID:      "usr-2",
			withAuthzCtx: withAllowAuthorizer,
			wantStatus:   http.StatusOK,
		},
		{
			// Admin cross-user query with a DENY Authorizer in context → 403.
			name:    "admin_cross_user_deny",
			subject: "admin-1",
			roles:   []string{"admin"},
			actorID: "usr-2",
			withAuthzCtx: func(ctx context.Context) context.Context {
				return withDenyAuthorizer(ctx, "policy: no matching allow rule")
			},
			wantStatus: http.StatusForbidden,
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
				// No-authz base: cases that exercise the fail-closed path rely on
				// the absence of a PDP; cases that expect success inject one via
				// withAuthzCtx (allow/deny).
				ctx := auditTestCtxNoAuthz(tc.subject, tc.roles)
				if tc.withAuthzCtx != nil {
					ctx = tc.withAuthzCtx(ctx)
				}
				req = req.WithContext(ctx)
			}
			mux.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

// TestHandler_RegisterRoutes_TenantScoped proves the audit query endpoint is
// tenant-scoped (epic #1337 PR-2a): a tenant-bearing caller now SUCCEEDS (200)
// but sees only its own tenant's audit rows. This replaced the PR-1 (#1339 F2)
// blanket 403 fail-closed gate. The List adapter passes the authenticated
// principal's tenant as the mandatory typed Store.Query tenant param (#1618), so
// admin-ness widens the actor axis but never the tenant axis.
func TestHandler_RegisterRoutes_TenantScoped(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	h := NewHandler(svc)

	const (
		tenantA = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
		tenantB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Two rows for usr-1 (one per tenant) + one tenant-A row for usr-2.
	for _, e := range []*ledger.Entry{
		{
			ID: "ts-a1", EventID: "evt-ts-a1", EventType: "event.test.v1",
			ActorID: "usr-1", TenantID: tenantA, Timestamp: base, Payload: []byte("{}"),
		},
		{
			ID: "ts-b1", EventID: "evt-ts-b1", EventType: "event.test.v1",
			ActorID: "usr-1", TenantID: tenantB, Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
		},
		{
			ID: "ts-a2", EventID: "evt-ts-a2", EventType: "event.test.v1",
			ActorID: "usr-2", TenantID: tenantA, Timestamp: base.Add(seedThirdEntryOffset), Payload: []byte("{}"),
		},
	} {
		require.NoError(t, store.Append(context.Background(), e))
	}

	mux := http.NewServeMux()
	require.NoError(t, h.RegisterRoutes(mux))

	t.Run("non_admin_self_scoped_to_own_tenant", func(t *testing.T) {
		// usr-1 in tenant-A, self-query: sees its tenant-A row, NOT its tenant-B row.
		p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "usr-1", Roles: []string{"viewer"}, TenantID: tenantA, AuthMethod: "test"}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil).
			WithContext(withAllowAuthorizer(auth.WithPrincipal(context.Background(), p)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "evt-ts-a1", "caller must see its own tenant's row")
		assert.NotContains(t, body, "evt-ts-b1", "caller must NOT see its other-tenant row")
	})

	t.Run("admin_global_actor_but_tenant_scoped", func(t *testing.T) {
		// admin in tenant-A: global actor scope (sees usr-1 AND usr-2) but ONLY
		// tenant-A rows — admin widens the actor axis, never the tenant axis.
		p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "admin-1", Roles: []string{"admin"}, TenantID: tenantA, AuthMethod: "test"}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil).
			WithContext(withAllowAuthorizer(auth.WithPrincipal(context.Background(), p)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "evt-ts-a1", "admin sees tenant-A usr-1 row")
		assert.Contains(t, body, "evt-ts-a2", "admin sees tenant-A usr-2 row (global actor scope)")
		assert.NotContains(t, body, "evt-ts-b1", "admin must NOT see the other tenant's row")
	})
}

// Trust boundary tests (#27q).
func TestHandleQuery_ActorBinding(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
			// F1: empty actorId is now a permissioned ledger read, not an implicit
			// self-read. A non-admin without audit:read (no Authorizer wired) is
			// denied; it reads its own rows by naming itself (next case).
			name:       "non-admin empty actorId requires permission (F1)",
			query:      "",
			subject:    "usr-1",
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
			wantCount:  -1,
		},
		{
			// Non-admin self-read via explicit actorId: exempt from the PDP, and
			// RowScope=self still scopes results. eventType filter narrows within
			// the caller's own rows (no bootstrap rows for usr-1 → count 0).
			name:         "non-admin self eventType remains self-scoped",
			query:        "?eventType=bootstrap.auth.fail&actorId=usr-1",
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
			withAuthzCtx: withAllowAuthorizer,
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
			// The "other actors" branch now routes through RequirePermission(PermAuditRead).
			// An ALLOW Authorizer in context is required for the request to succeed.
			name:         "other actorId with admin and allow authorizer",
			query:        "?actorId=usr-2",
			subject:      "admin-user",
			roles:        []string{"admin"},
			withAuthzCtx: withAllowAuthorizer,
			wantStatus:   http.StatusOK,
			wantCount:    1,
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
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
	req = req.WithContext(auditTestCtx("admin-user", []string{"admin"}))
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
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
	req = req.WithContext(auditTestCtx("usr-1", nil)) // non-admin
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
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
	req = req.WithContext(auditTestCtx("admin-user", []string{"admin"}))
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
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
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
	// The admin is querying actorId=usr-ep (another user): the "other actors" branch
	// is taken, so an ALLOW Authorizer must be in context for the PDP to grant access.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId=usr-ep&traceId=", nil)
	req = req.WithContext(withAllowAuthorizer(auditTestCtx("admin-user", []string{"admin"})))
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

// TestHandleQuery_MaskedFilterOracle_Rejected locks the F1 invariant (epic #1337
// PR-12): a caller may NOT use a query predicate on a column its row-visibility
// scope masks. Allowing such a filter would leak a match/no-match oracle — a
// non-admin (self) could binary-search the very traceId the response redacts; a
// device could enumerate the subject it cannot see. The matrix drives every
// (scope × filterable-maskable column) pair and asserts the gate rejects exactly
// the masked ones (403) and admits the visible ones (200). It also pins the
// relationship anti-drift: the maximal (device) mask rejects BOTH gated columns.
func TestHandleQuery_MaskedFilterOracle_Rejected(t *testing.T) {
	const deviceID = "dev-oracle"
	// Allow-all Authorizer so the empty-actorId read passes the PDP gate (F1) and
	// the test reaches the data-layer masked-filter gate it exercises.
	deviceCtx := withAllowAuthorizer(auth.WithPrincipal(context.Background(), auth.MustNewTestDevicePrincipal(deviceID, auditQueryTestTenant)))

	cases := []struct {
		name       string
		ctx        context.Context
		query      string
		wantStatus int
	}{
		// non-admin user → RowScopeSelf masks {correlationId, traceId}.
		{"self_traceId_filter_rejected", auditTestCtx("usr-1", nil), "traceId=trace-abc", http.StatusForbidden},
		// subjectId is NOT masked for self → the filter is admitted.
		{"self_subjectId_filter_allowed", auditTestCtx("usr-1", nil), "subjectId=victim", http.StatusOK},
		// device → RowScopeDevice masks {subjectId, correlationId, traceId}: BOTH gated.
		{"device_traceId_filter_rejected", deviceCtx, "traceId=trace-abc", http.StatusForbidden},
		{"device_subjectId_filter_rejected", deviceCtx, "subjectId=victim", http.StatusForbidden},
		// admin → RowScopeTenant masks nothing: every filter is admitted.
		{"admin_traceId_filter_allowed", auditTestCtx("admin-x", []string{auth.RoleAdmin}), "traceId=trace-abc", http.StatusOK},
		{"admin_subjectId_filter_allowed", auditTestCtx("admin-x", []string{auth.RoleAdmin}), "subjectId=victim", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newHandlerStore(t)
			svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
			require.NoError(t, err)
			mux := newHandlerMux(svc)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?"+tc.query, nil).WithContext(tc.ctx)
			mux.ServeHTTP(w, req)

			require.Equalf(t, tc.wantStatus, w.Code, "body=%s", w.Body.String())
			if tc.wantStatus == http.StatusForbidden {
				// The rejection must name the masked column (PublicString detail), not
				// leak whether any row matched — it fails before the store is queried.
				assert.Contains(t, w.Body.String(), "masked for your access scope",
					"403 must be the masked-column gate, not an unrelated forbidden")
			}
		})
	}
}

// TestHandleQuery_EmptyMaskedFilterParam_NotRejected verifies an EMPTY masked
// filter param (?traceId=) is treated as "no filter" and does NOT trip the F1
// gate — the oracle only exists for a non-empty predicate.
func TestHandleQuery_EmptyMaskedFilterParam_NotRejected(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?traceId=", nil)
	req = req.WithContext(auditTestCtx("usr-1", nil)) // non-admin (masks traceId)
	mux.ServeHTTP(w, req)

	require.Equalf(t, http.StatusOK, w.Code, "empty masked filter must act as no filter; body=%s", w.Body.String())
}

// TestHandleQuery_ColumnMaskMatrix is the per-principal column-masking matrix
// (epic #1337 PR-12, T12.4 unit-level counterpart): admin / non-admin user / device
// callers see DIFFERENT visible columns from auditFieldMask, discharged through the
// ResourceProjection funnel. Same row contents per owner axis isolate masking from
// data. admin → full view; non-admin self → correlationId+traceId masked; device →
// subjectId+correlationId+traceId masked. tenantId is visible to all (own-tenant
// row) — it is projected (PR-12) but not in any per-scope mask.
func TestHandleQuery_ColumnMaskMatrix(t *testing.T) {
	const (
		masked      = "<REDACTED>"
		selfSubject = "usr-self"
		deviceID    = "dev-7"
		subjectVal  = "subject-of-record"
	)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	// One row per owner axis (actor_id), each carrying identical sensitive column
	// values so the assertions isolate masking, not data. The owner column is
	// actor_id (vis.Allows(entry.ActorID)).
	seedRow := func(id, actor string) *ledger.Entry {
		return &ledger.Entry{
			ID: id, EventID: "evt-" + id, EventType: "event.test.v1",
			ActorID:       actor,
			SubjectID:     subjectVal,
			TenantID:      auditQueryTestTenant,
			CorrelationID: "corr-" + id,
			TraceID:       "trace-" + id,
			Timestamp:     base, OccurredAt: base,
			Payload: []byte("{}"),
		}
	}
	for _, e := range []*ledger.Entry{
		seedRow("admin", "admin-actor"),
		seedRow("self", selfSubject),
		seedRow("dev", deviceID),
	} {
		require.NoError(t, store.Append(context.Background(), e))
	}

	deviceCtx := withAllowAuthorizer(auth.WithPrincipal(context.Background(), auth.MustNewTestDevicePrincipal(deviceID, auditQueryTestTenant)))

	cases := []struct {
		name                             string
		ctx                              context.Context
		eventID                          string
		wantSubject, wantCorr, wantTrace string
	}{
		// admin (RowScopeTenant): full view — every column visible.
		{"admin_full_view", auditTestCtx("admin-x", []string{auth.RoleAdmin}), "evt-admin", subjectVal, "corr-admin", "trace-admin"},
		// non-admin user (RowScopeSelf): operator-diagnostic columns masked.
		{"non_admin_self_masks_diagnostics", auditTestCtx(selfSubject, nil), "evt-self", subjectVal, masked, masked},
		// device (RowScopeDevice): also masks the human subject-of-record.
		{"device_masks_subject_and_diagnostics", deviceCtx, "evt-dev", masked, masked, masked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil).WithContext(tc.ctx)
			mux.ServeHTTP(w, req)
			require.Equalf(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

			var resp struct {
				Data []map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

			var row map[string]any
			for _, r := range resp.Data {
				if r["eventId"] == tc.eventID {
					row = r
					break
				}
			}
			require.NotNilf(t, row, "row %s not visible to %s; body=%s", tc.eventID, tc.name, w.Body.String())

			assert.Equal(t, tc.wantSubject, row["subjectId"], "subjectId")
			assert.Equal(t, tc.wantCorr, row["correlationId"], "correlationId")
			assert.Equal(t, tc.wantTrace, row["traceId"], "traceId")
			// tenantId is projected (PR-12) and never in a per-scope mask: visible to all.
			assert.Equal(t, auditQueryTestTenant, row["tenantId"], "tenantId visible (own tenant)")

			// Raw-value non-leak assertions: the actual sensitive values for THIS row
			// must not appear anywhere in the body when the scope masks them.
			body := w.Body.String()
			switch tc.name {
			case "non_admin_self_masks_diagnostics":
				// self row's correlationId and traceId are masked; raw values must not leak.
				assert.NotContains(t, body, "corr-self", "corr-self raw value must not appear in self-scoped body")
				assert.NotContains(t, body, "trace-self", "trace-self raw value must not appear in self-scoped body")
			case "device_masks_subject_and_diagnostics":
				// device query is scoped to actor_id==deviceID, so only the device row is returned.
				require.Lenf(t, resp.Data, 1, "device scope must return exactly 1 row; body=%s", body)
				// device row's correlationId and traceId are masked; raw values must not leak.
				assert.NotContains(t, body, "corr-dev", "corr-dev raw value must not appear in device-scoped body")
				assert.NotContains(t, body, "trace-dev", "trace-dev raw value must not appear in device-scoped body")
				// subjectVal is unique to the device's own row in this single-row response.
				assert.NotContains(t, body, subjectVal, "subjectVal must not appear in device-scoped body (single-row response)")
			}
		})
	}
}

type actorBindingCase struct {
	name            string
	query           string
	subject         string
	roles           []string
	injectEmptyAuth bool
	// withAuthzCtx optionally wraps the built context to inject an Authorizer.
	// Required when the test case exercises the "other actors" PDP branch
	// (actorId set and != subject) and expects a 200 response.
	withAuthzCtx func(context.Context) context.Context
	wantStatus   int
	wantCount    int
	wantActorIDs []string
}

func assertActorBindingCase(t *testing.T, mux *http.ServeMux, tc actorBindingCase) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries"+tc.query, nil)
	switch {
	case tc.injectEmptyAuth:
		req = req.WithContext(auditTestCtxNoAuthz("", tc.roles))
	case tc.subject != "":
		// No-authz base: success cases inject a per-case Authorizer via withAuthzCtx;
		// fail-closed cases rely on its absence.
		ctx := auditTestCtxNoAuthz(tc.subject, tc.roles)
		if tc.withAuthzCtx != nil {
			ctx = tc.withAuthzCtx(ctx)
		}
		req = req.WithContext(ctx)
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

// TestAuditQueryPolicy is a direct table-driven unit test of the auditQueryPolicy
// function (F11/F3-test fix). It covers all branches of the policy gate without
// going through the full HTTP handler stack, so failures point precisely at the
// policy logic rather than at routing or codec layers.
//
// Design notes:
//   - Self branch (actorId == subject ONLY): returns nil immediately, no PDP call.
//     This is the explicit-self-read shape exemption.
//   - Permissioned branch (actorId empty, OR set and != subject): delegates to
//     auth.RequirePermission(authz.PermAuditRead()), which reads the Authorizer
//     from ctx. No Authorizer → fail-closed 403. Allow Authorizer → nil.
//     Deny Authorizer → 403. Authorizer returning error → error passes through
//     (status determined by the error's errcode Kind — e.g. KindUnavailable → 503).
//     F1: empty actorId is in the permissioned branch (a ledger-wide read for an
//     admin), NOT the self branch, so it cannot skip the PDP.
//   - auditQueryPolicy is NOT involved in row-visibility or column-masking;
//     those are enforced at the data layer (RowScope / FieldMask obligations).
func TestAuditQueryPolicy(t *testing.T) {
	const (
		selfSubject  = "usr-self"
		otherSubject = "usr-other"
	)

	unavailableErr := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
		"policy store unavailable")

	tests := []struct {
		name         string
		principal    *auth.Principal // nil = no principal in ctx
		actorID      string          // query param value
		withAuthzCtx func(context.Context) context.Context
		wantErr      bool
		wantErrCode  string // errcode.Code string, checked when wantErr=true
		wantErrNil   bool   // true = error must be nil (allow)
	}{
		{
			// No principal in context → 401 (auditQueryPolicy requires auth.FromContext ok).
			name:        "no_principal_returns_401",
			principal:   nil,
			actorID:     "",
			wantErr:     true,
			wantErrCode: "ERR_AUTH_UNAUTHORIZED",
		},
		{
			// F1: empty actorId is a permissioned read, NOT the self branch. With
			// no Authorizer in ctx it fails closed → 403 (cannot skip the PDP).
			name:        "empty_actorId_requires_permission_failclosed",
			principal:   &auth.Principal{Kind: auth.PrincipalUser, Subject: selfSubject, TenantID: auditQueryTestTenant, AuthMethod: "test"},
			actorID:     "",
			wantErr:     true,
			wantErrCode: "ERR_AUTH_FORBIDDEN",
		},
		{
			// F1: empty actorId with an Allow Authorizer in ctx → nil (the PDP
			// granted audit:read, e.g. an admin via the baseline).
			name:         "empty_actorId_with_allow_authorizer_permits",
			principal:    &auth.Principal{Kind: auth.PrincipalUser, Subject: selfSubject, TenantID: auditQueryTestTenant, AuthMethod: "test"},
			actorID:      "",
			withAuthzCtx: withAllowAuthorizer,
			wantErrNil:   true,
		},
		{
			// Self branch: actorId == subject → nil (allow), no PDP consulted.
			name:       "actorId_equals_subject_self_branch_allows",
			principal:  &auth.Principal{Kind: auth.PrincipalUser, Subject: selfSubject, TenantID: auditQueryTestTenant, AuthMethod: "test"},
			actorID:    selfSubject,
			wantErrNil: true,
		},
		{
			// Cross-actor: actorId != subject, no Authorizer in ctx → fail-closed 403.
			name:        "cross_actor_no_authorizer_failclosed",
			principal:   &auth.Principal{Kind: auth.PrincipalUser, Subject: selfSubject, TenantID: auditQueryTestTenant, AuthMethod: "test"},
			actorID:     otherSubject,
			wantErr:     true,
			wantErrCode: "ERR_AUTH_FORBIDDEN",
		},
		{
			// Cross-actor: actorId != subject, Allow Authorizer → nil (allow).
			name:         "cross_actor_allow_authorizer_permits",
			principal:    &auth.Principal{Kind: auth.PrincipalUser, Subject: selfSubject, TenantID: auditQueryTestTenant, AuthMethod: "test"},
			actorID:      otherSubject,
			withAuthzCtx: withAllowAuthorizer,
			wantErrNil:   true,
		},
		{
			// Cross-actor: actorId != subject, Deny Authorizer → 403.
			name:      "cross_actor_deny_authorizer_forbids",
			principal: &auth.Principal{Kind: auth.PrincipalUser, Subject: selfSubject, TenantID: auditQueryTestTenant, AuthMethod: "test"},
			actorID:   otherSubject,
			withAuthzCtx: func(ctx context.Context) context.Context {
				return withDenyAuthorizer(ctx, "policy: no matching allow rule")
			},
			wantErr:     true,
			wantErrCode: "ERR_AUTH_FORBIDDEN",
		},
		{
			// Cross-actor: Authorizer returns error (e.g. KindUnavailable) → error
			// passes through unchanged (503-mapping upstream in RequirePermission).
			name:      "cross_actor_authorizer_error_passes_through",
			principal: &auth.Principal{Kind: auth.PrincipalUser, Subject: selfSubject, TenantID: auditQueryTestTenant, AuthMethod: "test"},
			actorID:   otherSubject,
			withAuthzCtx: func(ctx context.Context) context.Context {
				return auth.WithAuthorizer(ctx, &mockAuthorizer{err: unavailableErr})
			},
			wantErr:     true,
			wantErrCode: "ERR_SERVICE_UNAVAILABLE",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/v1/audit/entries"
			if tc.actorID != "" {
				url += "?actorId=" + tc.actorID
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)

			// Build context.
			ctx := context.Background()
			if tc.principal != nil {
				ctx = auth.WithPrincipal(ctx, tc.principal)
			}
			if tc.withAuthzCtx != nil {
				ctx = tc.withAuthzCtx(ctx)
			}
			req = req.WithContext(ctx)

			err := auditQueryPolicy(req)

			if tc.wantErrNil {
				assert.NoError(t, err, "expected nil error (allow)")
				return
			}
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err, "expected a non-nil error")
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec, "error must be an *errcode.Error")
			assert.Equal(t, tc.wantErrCode, string(ec.Code),
				"error code mismatch for case %q", tc.name)
		})
	}
}

// TestHandleQuery_RowScopeVisibilityMatrix is the T5.5 e2e test for EPIC #1337
// PR-5 (#1343): identity → RowScope narrowing at the handler layer.
//
// Seed (three entries in a multi-tenant ledger):
//
//	entry vsm-a1: actor=usrA,           tenantID=auditQueryTestTenant   (tenantA)
//	entry vsm-b1: actor=usrB,           tenantID=auditQueryTestTenantB  (tenantB)
//	entry vsm-sys: actor="system:bootstrap", tenantID=""                (tenant-less)
//
// Expected visibility matrix (no actorId query param — handler derives from identity):
//
//	non-admin usrA in tenantA → count 1  (RowScopeSelf, own row only)
//	admin in tenantA           → count 2  (RowScopeTenant: tenantA + tenant-less; NOT tenantB)
//	super-admin (any tenant)   → 501 when no CrossTenantQueryStore wired (graceful-absent
//	                             path, per ADR #1810); the 200 path is covered by
//	                             TestHandleQuery_SuperAdmin_CrossTenantStore_200.
//
// The 501 in the super-admin case is the graceful-absent path (no admin pool provisioned),
// not a capability deferral: when CrossTenantQueryStore is nil, the handler returns
// RowScopeAllUnsupportedError (501 Not Implemented, per ADR #1810 §graceful-absent).
// When a CrossTenantQueryStore IS wired, the handler routes to the dedicated admin pool
// and returns 200 with cross-tenant rows (see TestHandleQuery_SuperAdmin_CrossTenantStore_200).
// 501 (not 500): the super-admin request is policy-authorized but the capability is
// unavailable due to absent wiring (RFC 9110 §15.6.2). The mandatory FR-007 slog.Error
// audit is still emitted inside p.CrossTenantVisibility before the store rejects, so the
// FR-007 assertion holds regardless of the store outcome.
func TestHandleQuery_RowScopeVisibilityMatrix(t *testing.T) {
	// Install slog capture to assert FR-007: super-admin cross-tenant access must
	// emit a slog.Error record; admin and non-admin paths must not.
	capture := &testCaptureHandler{}
	slogcapture.InstallDefault(t, slog.New(capture))

	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Seed: one entry per tenant + one tenant-less system entry.
	seed := []*ledger.Entry{
		{
			ID: "vsm-a1", EventID: "evt-vsm-a1", EventType: "vis.matrix.v1",
			ActorID: "usrA", TenantID: auditQueryTestTenant,
			Timestamp: base, Payload: []byte("{}"),
		},
		{
			ID: "vsm-b1", EventID: "evt-vsm-b1", EventType: "vis.matrix.v1",
			ActorID: "usrB", TenantID: auditQueryTestTenantB,
			Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
		},
		{
			// Tenant-less system entry: TenantID="" so the typed-tenant query
			// includes it for any non-empty tenant (tenant-less system rows are
			// visible to every tenant, not a specific tenant's private data).
			ID: "vsm-sys", EventID: "evt-vsm-sys", EventType: "vis.matrix.v1",
			ActorID:   "system:bootstrap",
			TenantID:  "",
			Timestamp: base.Add(seedThirdEntryOffset), Payload: []byte("{}"),
		},
	}
	for _, e := range seed {
		require.NoError(t, store.Append(context.Background(), e))
	}

	cases := []auditVisibilityCase{
		{
			// non-admin usrA: RowScopeSelf → sees only its own row (vsm-a1). The
			// system row (actor=system:bootstrap) is filtered out by the owner axis.
			name:          "non_admin_self_scope",
			subject:       "usrA",
			roles:         nil,
			tenantID:      auditQueryTestTenant,
			wantCount:     1,
			wantSystemRow: false,
		},
		{
			// admin in tenantA: RowScopeTenant → sees tenantA rows + tenant-less
			// system row (marked scope="system"). The vsm-b1 (tenantB) row must NOT
			// be visible.
			name:          "admin_tenant_scope",
			subject:       "admin-a",
			roles:         []string{auth.RoleAdmin},
			tenantID:      auditQueryTestTenant,
			wantCount:     2,
			wantSystemRow: true,
		},
		{
			// super-admin: RowScopeAll is fail-closed under #1618 FORCE RLS
			// (deferred) — the handler returns 501. Must still trigger exactly one
			// FR-007 slog.Error audit record (emitted before the store rejects).
			name:      "superadmin_cross_tenant_failclosed",
			subject:   "super-sa",
			roles:     []string{auth.RoleSuperAdmin},
			tenantID:  auditQueryTestTenant,
			wantCount: 0, // unused: super-admin asserts a 501, not a row count
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture.records = capture.records[:0] // reset between sub-tests

			assertAuditVisibilityCase(t, mux, tc)
			errorCount := countAuditMandatoryRecords(capture.records)
			wantError := isSuperAdmin(tc.roles)
			if wantError && errorCount == 0 {
				t.Errorf("tc=%s: expected FR-007 slog.Error audit record with {actor,scope,tenant,reason}, got 0 (total records: %d)",
					tc.name, len(capture.records))
			}
			if !wantError && errorCount > 0 {
				t.Errorf("tc=%s: expected no FR-007 Error-level records, got %d", tc.name, errorCount)
			}
		})
	}
}

// newSuperAdminCtx builds a tenant-bearing super-admin principal context with
// auditQueryTestTenant as the token tenant (super-admins still carry their own
// tenant_id in the JWT — it feeds FR-007 audit fields, not the query scope).
func newSuperAdminCtx(subject string) context.Context {
	return withAllowAuthorizer(auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    subject,
		Roles:      []string{auth.RoleSuperAdmin},
		TenantID:   auditQueryTestTenant,
		AuthMethod: "test",
	}))
}

// TestHandleQuery_SuperAdmin_NoCrossTenantStore_501 locks the pre-#1810
// fail-closed contract: a super-admin request with no CrossTenantQueryStore
// wired returns 501 (RowScopeAllUnsupportedError), never 200 or 500.
func TestHandleQuery_SuperAdmin_NoCrossTenantStore_501(t *testing.T) {
	store := newHandlerStore(t)
	// NewService without WithCrossTenantStore → crossTenantStore is nil
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(newSuperAdminCtx("sa-user"))
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code,
		"super-admin with no CrossTenantQueryStore must return 501; body=%s", w.Body.String())
}

// TestHandleQuery_SuperAdmin_CrossTenantStore_200 verifies that when a
// CrossTenantQueryStore is wired, a super-admin request returns 200 with rows
// spanning more than one tenant (#1810). The fake store returns entries from
// two distinct tenants so the test proves cross-tenant data is surfaced.
func TestHandleQuery_SuperAdmin_CrossTenantStore_200(t *testing.T) {
	base := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	ctEntries := []*ledger.Entry{
		{
			ID: "ct-a1", EventID: "evt-ct-a1", EventType: "cross.tenant.v1",
			ActorID: "usrA", TenantID: auditQueryTestTenant,
			Timestamp: base, Payload: []byte("{}"),
		},
		{
			ID: "ct-b1", EventID: "evt-ct-b1", EventType: "cross.tenant.v1",
			ActorID: "usrB", TenantID: auditQueryTestTenantB,
			Timestamp: base.Add(time.Hour), Payload: []byte("{}"),
		},
	}

	store := newHandlerStore(t)
	// Build a MemCrossTenantStore backed by the same store — for the handler test
	// we use the fake store interface that directly returns ctEntries.
	relay := newHandlerStore(t)
	for _, e := range ctEntries {
		require.NoError(t, relay.Append(context.Background(), e))
	}
	ctStore, err := ledger.NewMemCrossTenantStore(relay)
	require.NoError(t, err)

	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, WithCrossTenantStore(ctStore))
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(newSuperAdminCtx("sa-user"))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"super-admin with CrossTenantQueryStore must return 200; body=%s", w.Body.String())

	var resp struct {
		Data []struct {
			TenantID string `json:"tenantId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2, "expected entries from both tenants")
	tenants := map[string]bool{}
	for _, d := range resp.Data {
		tenants[d.TenantID] = true
	}
	assert.True(t, tenants[auditQueryTestTenant], "tenantA rows must be present")
	assert.True(t, tenants[auditQueryTestTenantB], "tenantB rows must be present")
}

// TestHandleQuery_SuperAdmin_SingleAuditRecord is the MANDATORY single-audit
// invariant test (#1810): exactly ONE FR-007 slog.Error record must be emitted
// per super-admin cross-tenant request, whether or not the CrossTenantQueryStore
// is present. Double-audit (calling both p.CrossTenantVisibility and p.RowVisibility
// for the same request) would emit two records — this test guards against that.
//
// The test captures slog records via the testCaptureHandler and asserts the count
// is exactly 1 after each super-admin request.
func TestHandleQuery_SuperAdmin_SingleAuditRecord(t *testing.T) {
	capture := &testCaptureHandler{}
	slogcapture.InstallDefault(t, slog.New(capture))

	store := newHandlerStore(t)
	relay := newHandlerStore(t)
	ctStore, err := ledger.NewMemCrossTenantStore(relay)
	require.NoError(t, err)

	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, WithCrossTenantStore(ctStore))
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	cases := []struct {
		name     string
		ctStore  bool // whether ctStore is wired
		wantCode int
	}{
		{"with_ct_store_200", true, http.StatusOK},
	}
	// Also test without store (501 path still audits once)
	{
		svcNoStore, err2 := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
		require.NoError(t, err2)
		muxNoStore := newHandlerMux(svcNoStore)

		capture.records = capture.records[:0]
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
		req = req.WithContext(newSuperAdminCtx("sa-no-store"))
		muxNoStore.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotImplemented, w.Code)

		errorCount := countAuditMandatoryRecords(capture.records)
		assert.Equal(t, 1, errorCount,
			"super-admin cross-tenant request (no store, 501) must emit EXACTLY ONE FR-007 Error record; got %d (records: %v)",
			errorCount, capture.records)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture.records = capture.records[:0]

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
			req = req.WithContext(newSuperAdminCtx("sa-user"))
			mux.ServeHTTP(w, req)
			assert.Equal(t, tc.wantCode, w.Code,
				"unexpected status; body=%s", w.Body.String())

			errorCount := countAuditMandatoryRecords(capture.records)
			assert.Equal(t, 1, errorCount,
				"super-admin cross-tenant request must emit EXACTLY ONE FR-007 slog.Error record; got %d", errorCount)
		})
	}
}

// --- F1: Cross-tenant path always requires audit:read (Codex review #2051) ---

// TestHandleQuery_SuperAdmin_SelfActorId_DenyPDP_Returns403 is the F1 regression
// test: a super-admin with ?actorId=<self> + a deny Authorizer (no audit:read)
// must receive 403, not a cross-tenant 200. Without the fix, auditQueryPolicy's
// self-read exemption (actorId==subject → return nil) bypasses the PDP entirely,
// and the cross-tenant read proceeds even though the PDP would deny it.
func TestHandleQuery_SuperAdmin_SelfActorId_DenyPDP_Returns403(t *testing.T) {
	base := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	ctEntries := []*ledger.Entry{
		{
			ID: "f1-a1", EventID: "evt-f1-a1", EventType: "f1.test.v1",
			ActorID: "sa-user", TenantID: auditQueryTestTenant,
			Timestamp: base, Payload: []byte("{}"),
		},
	}
	relay := newHandlerStore(t)
	for _, e := range ctEntries {
		require.NoError(t, relay.Append(context.Background(), e))
	}
	ctStore, err := ledger.NewMemCrossTenantStore(relay)
	require.NoError(t, err)

	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, WithCrossTenantStore(ctStore))
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	// Super-admin with a deny Authorizer: the PDP explicitly rejects audit:read.
	// Use ?actorId=sa-user (== subject) to trigger the route-level self-read exemption,
	// which without the F1 fix would let the cross-tenant read bypass the PDP entirely.
	subject := "sa-user"
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    subject,
		Roles:      []string{auth.RoleSuperAdmin},
		TenantID:   auditQueryTestTenant,
		AuthMethod: "test",
	})
	ctx = withDenyAuthorizer(ctx, "policy: audit:read denied by tenant policy")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId="+subject, nil)
	req = req.WithContext(ctx)
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code,
		"super-admin with ?actorId=<self> + deny PDP must return 403 (F1); body=%s", w.Body.String())
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ERR_AUTH_FORBIDDEN", resp.Error.Code)
}

// TestHandleQuery_SuperAdmin_SelfActorId_AllowPDP_Returns200 verifies the positive
// case: a super-admin with ?actorId=<self> + an allow Authorizer (audit:read granted)
// succeeds with 200 and returns cross-tenant rows.
func TestHandleQuery_SuperAdmin_SelfActorId_AllowPDP_Returns200(t *testing.T) {
	base := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	subject := "sa-user-allow"
	ctEntries := []*ledger.Entry{
		{
			ID: "f1-allow-a1", EventID: "evt-f1-allow-a1", EventType: "f1.allow.v1",
			ActorID: subject, TenantID: auditQueryTestTenant,
			Timestamp: base, Payload: []byte("{}"),
		},
	}
	relay := newHandlerStore(t)
	for _, e := range ctEntries {
		require.NoError(t, relay.Append(context.Background(), e))
	}
	ctStore, err := ledger.NewMemCrossTenantStore(relay)
	require.NoError(t, err)

	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, WithCrossTenantStore(ctStore))
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    subject,
		Roles:      []string{auth.RoleSuperAdmin},
		TenantID:   auditQueryTestTenant,
		AuthMethod: "test",
	})
	ctx = withAllowAuthorizer(ctx)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId="+subject, nil)
	req = req.WithContext(ctx)
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"super-admin with ?actorId=<self> + allow PDP must return 200 (F1 positive); body=%s", w.Body.String())
}

// --- F10: Cross-tenant 200 path must pass filters to the store (Codex review #2051) ---

// filterCaptureCTStore is a CrossTenantQueryStore that records the AuditFilters it
// receives, enabling F10 assertions.
type filterCaptureCTStore struct {
	capturedFilters ledger.AuditFilters
}

func (f *filterCaptureCTStore) QueryCrossTenant(
	_ context.Context, _ tenant.CrossTenantVisibility,
	filters ledger.AuditFilters, params query.ListParams,
) ([]*ledger.Entry, error) {
	f.capturedFilters = filters
	return []*ledger.Entry{}, nil
}

// GetByIDCrossTenant satisfies CrossTenantQueryStore; the single-entry cross-tenant
// path has its own dedicated handler tests, so this stub just returns not-found.
func (f *filterCaptureCTStore) GetByIDCrossTenant(
	_ context.Context, _ tenant.CrossTenantVisibility, _ string,
) (*ledger.Entry, error) {
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
		"audit ledger: entry not found")
}

// TestHandleQuery_SuperAdmin_CrossTenant_FiltersPassthrough asserts that every
// query filter (actorId, subjectId, traceId, eventType, from, to) reaches the
// CrossTenantQueryStore unchanged on the 200 path (F10, Codex review).
// Previously the test only asserted the 200 status; the filter passthrough was not
// verified, so a mistaken filter drop would be invisible.
func TestHandleQuery_SuperAdmin_CrossTenant_FiltersPassthrough(t *testing.T) {
	capStore := &filterCaptureCTStore{}
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, WithCrossTenantStore(capStore))
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	fromStr := "2026-06-01T00:00:00Z"
	toStr := "2026-06-30T23:59:59Z"
	url := "/api/v1/audit/entries" +
		"?actorId=actor-1" +
		"&subjectId=subject-2" +
		"&traceId=trace-abc" +
		"&eventType=some.event.v1" +
		"&from=" + fromStr +
		"&to=" + toStr

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = req.WithContext(newSuperAdminCtx("sa-filter-test"))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"filter-passthrough test must return 200; body=%s", w.Body.String())

	filters := capStore.capturedFilters
	assert.Equal(t, "actor-1", filters.ActorID, "actorId must reach the cross-tenant store")
	assert.Equal(t, "subject-2", filters.SubjectID, "subjectId must reach the cross-tenant store")
	assert.Equal(t, "trace-abc", filters.TraceID, "traceId must reach the cross-tenant store")
	assert.Equal(t, "some.event.v1", filters.EventType, "eventType must reach the cross-tenant store")

	wantFrom := "2026-06-01T00:00:00Z"
	assert.Equal(t, wantFrom, filters.From.UTC().Format(time.RFC3339), "from must reach the cross-tenant store")
	wantTo := "2026-06-30T23:59:59Z"
	assert.Equal(t, wantTo, filters.To.UTC().Format(time.RFC3339), "to must reach the cross-tenant store")
}

// --- Issue #2199: empty Payload must not cause 5xx ---

// TestHandleQuery_EmptyPayload_Returns200 is the regression guard for #2199:
// an audit entry with nil/empty Payload must not cause 5xx when ToMap tries to
// marshal an empty json.RawMessage. The fix ensures toListResponseDataItem leaves
// the Payload field nil when the redacted bytes are empty, so ToMap omits the key
// and json.Marshal never sees an empty RawMessage.
//
// Additionally, items WITH a non-empty payload must still render correctly in the
// same response.
func TestHandleQuery_EmptyPayload_Returns200(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Entry with nil payload (pre-populated rows or framework events may have none).
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "ep-nil-1", EventID: "evt-ep-nil-1", EventType: "event.test.v1",
		ActorID:   "usr-ep",
		Timestamp: base,
		Payload:   nil, // empty / absent payload
	}))
	// Entry with empty-slice payload.
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "ep-nil-2", EventID: "evt-ep-nil-2", EventType: "event.test.v1",
		ActorID:   "usr-ep",
		Timestamp: base.Add(time.Minute),
		Payload:   []byte{}, // zero-length payload
	}))
	// Entry with a non-empty payload — must still render the payload key.
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "ep-data-3", EventID: "evt-ep-data-3", EventType: "event.test.v1",
		ActorID:   "usr-ep",
		Timestamp: base.Add(seedThirdEntryOffset),
		Payload:   []byte(`{"k":"v"}`),
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?actorId=usr-ep", nil)
	req = req.WithContext(auditTestCtx("usr-ep", nil))
	mux.ServeHTTP(w, req)

	require.Equalf(t, http.StatusOK, w.Code, "#2199: empty-payload entry must not cause 5xx; body=%s", w.Body.String())

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 3, "all three entries must be present")

	for _, item := range resp.Data {
		eventID, _ := item["eventId"].(string)
		if eventID == "evt-ep-data-3" {
			// Non-empty payload must appear as a valid JSON value.
			_, hasPayload := item["payload"]
			assert.True(t, hasPayload, "item with non-empty payload must include 'payload' key")
		} else {
			// Empty-payload items must NOT include the 'payload' key (omit nil).
			_, hasPayload := item["payload"]
			assert.False(t, hasPayload, "item %s with empty payload must NOT include 'payload' key", eventID)
		}
	}
}

// --- Issue #1742: actorId/subjectId/traceId/eventType input validation ---

// maxIDLen matches idutil.MaxMetadataIDLen (256). Redeclared here as a
// test-local const rather than importing idutil so tests stay in the auditquery
// package and match the "mock in same-package test file" convention. The actual
// enforcement uses idutil.SafeID(x).Validate() whose cap is MaxMetadataIDLen.
const testMaxIDLen = 256

// TestHandleQuery_FilterValidation_IDFormats is a table-driven test that verifies
// the wire-boundary validation introduced for #1742 (CWE-117 log injection + SQL
// predicate hygiene). Invalid actorId/subjectId/traceId (too long or unsafe chars)
// must return 400 BEFORE any logging of the untrusted input occurs. Valid inputs
// must pass through and produce 200.
//
// Critically: the validation MUST run BEFORE logAdminAuditQuery logs req.ActorID
// (CWE-117 — never log unvalidated input). The ordering is: validate → log →
// query. A 400 for an invalid filter means the log never fires.
func TestHandleQuery_FilterValidation_IDFormats(t *testing.T) {
	store := newHandlerStore(t)
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	// Seed one entry so a valid query returns 200 with data.
	base := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Append(context.Background(), &ledger.Entry{
		ID: "fv-1", EventID: "evt-fv-1", EventType: "event.test.v1",
		ActorID: "admin-user", Timestamp: base, Payload: []byte("{}"),
	}))

	tooLong := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}

	tests := []struct {
		name       string
		param      string // query param to set
		value      string
		wantStatus int
		wantCode   string // error code in JSON when wantStatus != 200
	}{
		// --- actorId ---
		{
			name:  "actorId too long",
			param: "actorId", value: tooLong(testMaxIDLen + 1),
			wantStatus: http.StatusBadRequest, wantCode: "ERR_VALIDATION_FAILED",
		},
		// Space is outside the SafeID charset (ASCII letters, digits, ._:/-).
		{
			name:  "actorId unsafe chars (space)",
			param: "actorId", value: "usr injection",
			wantStatus: http.StatusBadRequest, wantCode: "ERR_VALIDATION_FAILED",
		},
		// '@' is outside the SafeID charset.
		{
			name:  "actorId unsafe chars (at-sign)",
			param: "actorId", value: "usr@injection",
			wantStatus: http.StatusBadRequest, wantCode: "ERR_VALIDATION_FAILED",
		},
		{
			name:  "actorId valid (self)",
			param: "actorId", value: "admin-user",
			wantStatus: http.StatusOK,
		},
		{
			// Exactly at MaxMetadataIDLen (256) is allowed; one over is rejected.
			name:  "actorId exactly at max len is valid",
			param: "actorId", value: tooLong(testMaxIDLen),
			wantStatus: http.StatusOK,
		},
		// --- subjectId ---
		{
			name:  "subjectId too long",
			param: "subjectId", value: tooLong(testMaxIDLen + 1),
			wantStatus: http.StatusBadRequest, wantCode: "ERR_VALIDATION_FAILED",
		},
		{
			name:  "subjectId unsafe chars (bracket)",
			param: "subjectId", value: "subject[injection]",
			wantStatus: http.StatusBadRequest, wantCode: "ERR_VALIDATION_FAILED",
		},
		{
			name:  "subjectId valid",
			param: "subjectId", value: "victim-user",
			wantStatus: http.StatusOK,
		},
		// --- traceId ---
		{
			name:  "traceId too long",
			param: "traceId", value: tooLong(testMaxIDLen + 1),
			wantStatus: http.StatusBadRequest, wantCode: "ERR_VALIDATION_FAILED",
		},
		{
			name:  "traceId unsafe chars (semicolon)",
			param: "traceId", value: "trace;injection",
			wantStatus: http.StatusBadRequest, wantCode: "ERR_VALIDATION_FAILED",
		},
		{
			name:  "traceId valid",
			param: "traceId", value: "trace-abc-123",
			wantStatus: http.StatusOK,
		},
		// --- eventType: length cap only (dotted label, not SafeID charset) ---
		{
			name:  "eventType too long (length cap)",
			param: "eventType", value: tooLong(testMaxIDLen + 1),
			wantStatus: http.StatusBadRequest, wantCode: "ERR_VALIDATION_FAILED",
		},
		{
			name:  "eventType valid dotted label",
			param: "eventType", value: "some.event.v1",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Build the request using net/url.Values to correctly percent-encode
			// special characters (spaces, brackets, etc.) in query param values,
			// avoiding httptest.NewRequest panicking on raw control characters.
			// For non-self actorId we need an admin with allow-all authorizer.
			ctx := auditTestCtx("admin-user", []string{"admin"})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
			// Set RawQuery after construction so the value is properly encoded.
			qv := req.URL.Query()
			qv.Set(tc.param, tc.value)
			req.URL.RawQuery = qv.Encode()
			req = req.WithContext(ctx)
			mux.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code, "case %q body=%s", tc.name, w.Body.String())
			if tc.wantCode != "" {
				var resp struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tc.wantCode, resp.Error.Code, "case %q", tc.name)
			}
		})
	}
}

// TestHandleQuery_FilterValidation_ValidationBeforeLogging asserts the CWE-117
// ordering: when actorId is invalid, the handler must return 400 WITHOUT logging
// the untrusted actorId value. This is a smoke test for the correct call order
// (validate → log, never log → validate).
func TestHandleQuery_FilterValidation_ValidationBeforeLogging(t *testing.T) {
	store := newHandlerStore(t)

	capture := &testCaptureHandler{}
	log := slog.New(capture)

	svc, err := NewService(store, testCodec(), log, outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	// Use a malformed actorId (unsafe chars — '@' is outside SafeID charset)
	// so validation returns 400 before logging.
	malformedActor := "actor@injection"

	// Admin context so logAdminAuditQuery would fire IF we got past validation.
	ctx := auditTestCtx("admin-user", []string{"admin"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	qv := req.URL.Query()
	qv.Set("actorId", malformedActor)
	req.URL.RawQuery = qv.Encode()
	req = req.WithContext(ctx)
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, "invalid actorId must return 400; body=%s", w.Body.String())

	// No log record should contain the malformed actor value — if validation fired
	// before logging, logAdminAuditQuery was never called with the bad input.
	for _, rec := range capture.records {
		rec.Attrs(func(a slog.Attr) bool {
			assert.NotContains(t, a.Value.String(), malformedActor,
				"malformed actorId must not appear in any log record (CWE-117)")
			return true
		})
	}
}

// TestList_NilLogger_AdminQuery_NoPanic is the regression guard for the codex F1
// finding: the admin audit-query breadcrumb (logAdminAuditQuery) switched from the
// nil-safe package-level slog.InfoContext to an injected logger.InfoContext, so a
// Service constructed with a nil logger would nil-panic on the admin path. NewService
// now normalizes a nil logger to slog.Default(), so the dereference is safe.
//
// The admin-with-empty-actorId case is the exact branch that panicked: it hits
// `case actorIDFilter == "":` → logger.InfoContext("audit: admin querying all actors").
func TestList_NilLogger_AdminQuery_NoPanic(t *testing.T) {
	store := newHandlerStore(t)

	// nil logger: pre-fix this nil-panics inside logAdminAuditQuery.InfoContext.
	svc, err := NewService(store, testCodec(), nil, outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)
	mux := newHandlerMux(svc)

	// Admin with NO actorId filter → logAdminAuditQuery fires the "querying all
	// actors" breadcrumb, dereferencing the (formerly nil) injected logger.
	ctx := auditTestCtx("admin-user", []string{"admin"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(ctx)

	require.NotPanics(t, func() { mux.ServeHTTP(w, req) },
		"admin audit query must not panic when the Service was built with a nil logger")
	assert.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
}

// --- http.audit.get.v1 (#1852) single-entry detail ---

// seedAuditEntry appends an entry and returns its store-assigned id (= EventID on
// the mem store, written back by Append). Used by the GetByID handler tests.
func seedAuditEntry(t *testing.T, store *ledger.MemStore, e *ledger.Entry) string {
	t.Helper()
	require.NoError(t, store.Append(context.Background(), e))
	require.NotEmpty(t, e.ID, "Append must write back the store id")
	return e.ID
}

func getByIDService(t *testing.T, store *ledger.MemStore, opts ...ServiceOption) http.Handler {
	t.Helper()
	svc, err := NewService(store, testCodec(), slog.Default(), outbox.DemoCellTxManager(),
		query.RunModeProd, opts...)
	require.NoError(t, err)
	return newHandlerMux(svc)
}

func getByID(mux http.Handler, ctx context.Context, id string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries/"+id, nil)
	mux.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func TestHandleGetByID_Admin_Found(t *testing.T) {
	store := newHandlerStore(t)
	occurred := time.Date(2026, 6, 1, 2, 3, 4, 123456789, time.UTC)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-detail-1", EventType: "audit.detail.v1", ActorID: "usr-actor",
		TenantID: auditQueryTestTenant, CorrelationID: "corr-visible",
		OccurredAt: occurred, Timestamp: time.Now().UTC(), Payload: []byte(`{"k":"v"}`),
	})
	mux := getByIDService(t, store)

	w := getByID(mux, auditTestCtx("admin-user", []string{"admin"}), id)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Data struct {
			ID            string `json:"id"`
			EventID       string `json:"eventId"`
			ActorID       string `json:"actorId"`
			CorrelationID string `json:"correlationId"`
			OccurredAt    string `json:"occurredAt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, id, resp.Data.ID)
	assert.Equal(t, "evt-detail-1", resp.Data.EventID)
	assert.Equal(t, "usr-actor", resp.Data.ActorID)
	// admin (RowScopeTenant) → identity mask → diagnostic columns visible.
	assert.Equal(t, "corr-visible", resp.Data.CorrelationID,
		"admin must see the correlationId column unmasked")
	// Non-zero OccurredAt is projected as RFC3339Nano (toGetResponseDataItem).
	assert.Equal(t, occurred.Format(time.RFC3339Nano), resp.Data.OccurredAt)
}

func TestHandleGetByID_NotFound(t *testing.T) {
	store := newHandlerStore(t)
	mux := getByIDService(t, store)
	w := getByID(mux, auditTestCtx("admin-user", []string{"admin"}),
		"00000000-0000-0000-0000-000000000000")
	require.Equal(t, http.StatusNotFound, w.Code, "body=%s", w.Body.String())
}

func TestHandleGetByID_MalformedID_BadRequest(t *testing.T) {
	store := newHandlerStore(t)
	mux := getByIDService(t, store)
	// '@' is outside the idutil.SafeID charset → 400 at the adapter charset gate.
	w := getByID(mux, auditTestCtx("admin-user", []string{"admin"}), "bad@id")
	require.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
}

func TestHandleGetByID_EmptyTenant_Forbidden(t *testing.T) {
	store := newHandlerStore(t)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-detail-nt", EventType: "audit.detail.v1", ActorID: "usr",
		TenantID: auditQueryTestTenant, Timestamp: time.Now().UTC(), Payload: []byte(`{}`),
	})
	mux := getByIDService(t, store)
	// Principal with empty TenantID; allow authorizer so the gate passes and the
	// adapter's tenant-isolation check is what fail-closes (403).
	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "u", Roles: []string{"admin"}, AuthMethod: "test"}
	ctx := withAllowAuthorizer(auth.WithPrincipal(context.Background(), p))
	w := getByID(mux, ctx, id)
	require.Equal(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
}

func TestHandleGetByID_AuditReadDenied_Forbidden(t *testing.T) {
	store := newHandlerStore(t)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-detail-deny", EventType: "audit.detail.v1", ActorID: "usr",
		TenantID: auditQueryTestTenant, Timestamp: time.Now().UTC(), Payload: []byte(`{}`),
	})
	mux := getByIDService(t, store)
	// Deny authorizer: the flat audit:read route gate denies → 403 (no actorId-self
	// exemption like the list, because the path param is the entry id).
	ctx := withDenyAuthorizer(auditTestCtxNoAuthz("usr", []string{"user"}), "no audit:read")
	w := getByID(mux, ctx, id)
	require.Equal(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
}

func TestHandleGetByID_SelfReadsOtherActor_NotFound(t *testing.T) {
	// IDOR-safe: a RowScopeSelf caller (non-admin) with audit:read granted passes the
	// gate but only resolves entries whose actor_id is itself; another actor's entry
	// collapses to 404 (not 403), never leaking existence (D3: the flat gate does not
	// widen data access — RowScope governs it).
	store := newHandlerStore(t)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-detail-alice", EventType: "audit.detail.v1", ActorID: "alice",
		TenantID: auditQueryTestTenant, Timestamp: time.Now().UTC(), Payload: []byte(`{}`),
	})
	mux := getByIDService(t, store)
	// subject "bob" with audit:read granted (allow), role user → RowScope=self.
	w := getByID(mux, auditTestCtx("bob", []string{"user"}), id)
	require.Equal(t, http.StatusNotFound, w.Code, "body=%s", w.Body.String())
}

func TestHandleGetByID_SelfFieldMaskApplied(t *testing.T) {
	// A self caller reading its OWN entry sees the diagnostic columns
	// (correlationId, traceId) masked, identical to the list read.
	store := newHandlerStore(t)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-detail-mask", EventType: "audit.detail.v1", ActorID: "alice",
		TenantID: auditQueryTestTenant, CorrelationID: "corr-secret", TraceID: "trace-secret",
		Timestamp: time.Now().UTC(), Payload: []byte(`{}`),
	})
	mux := getByIDService(t, store)
	w := getByID(mux, auditTestCtx("alice", []string{"user"}), id)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	body := w.Body.String()
	assert.NotContains(t, body, "corr-secret", "correlationId must be masked for a self caller")
	assert.NotContains(t, body, "trace-secret", "traceId must be masked for a self caller")
}

func TestHandleGetByID_PayloadRedacted(t *testing.T) {
	store := newHandlerStore(t)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-detail-pay", EventType: "audit.detail.v1", ActorID: "usr",
		TenantID: auditQueryTestTenant, Timestamp: time.Now().UTC(),
		Payload: []byte(`{"password":"hunter2"}`),
	})
	mux := getByIDService(t, store)
	w := getByID(mux, auditTestCtx("admin-user", []string{"admin"}), id)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.NotContains(t, w.Body.String(), "hunter2",
		"sensitive payload values must be redacted in the detail response")
}

func TestHandleGetByID_SuperAdmin_CrossTenant_Found(t *testing.T) {
	base := time.Now().UTC()
	e := &ledger.Entry{
		EventID: "evt-ct-detail-a1", EventType: "cross.tenant.detail.v1",
		ActorID: "usrA", TenantID: auditQueryTestTenant, Timestamp: base, Payload: []byte(`{}`),
	}
	relay := newHandlerStore(t)
	require.NoError(t, relay.Append(context.Background(), e))
	ctStore, err := ledger.NewMemCrossTenantStore(relay)
	require.NoError(t, err)

	mux := getByIDService(t, newHandlerStore(t), WithCrossTenantStore(ctStore))
	w := getByID(mux, newSuperAdminCtx("sa-user"), e.ID)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Data struct {
			EventID  string `json:"eventId"`
			TenantID string `json:"tenantId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "evt-ct-detail-a1", resp.Data.EventID)
	assert.Equal(t, auditQueryTestTenant, resp.Data.TenantID)
}

func TestHandleGetByID_SuperAdmin_NoCrossTenantStore_501(t *testing.T) {
	store := newHandlerStore(t)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-detail-sa", EventType: "audit.detail.v1", ActorID: "usr",
		TenantID: auditQueryTestTenant, Timestamp: time.Now().UTC(), Payload: []byte(`{}`),
	})
	// base service: no WithCrossTenantStore → super-admin RowScopeAll fail-closes 501
	// (graceful-absent, ADR #1810), exactly like the list read.
	mux := getByIDService(t, store)
	w := getByID(mux, newSuperAdminCtx("sa-user"), id)
	require.Equal(t, http.StatusNotImplemented, w.Code, "body=%s", w.Body.String())
}

// TestHandleGetByID_SuperAdmin_SingleAuditRecord pins the FR-007 mandatory
// cross-tenant audit invariant for the detail path (mirrors the list's
// TestHandleQuery_SuperAdmin_SingleAuditRecord): EVERY super-admin GetByID request
// emits EXACTLY ONE FR-007 slog.Error (via deriveAuditVisibility → single-mint
// p.CrossTenantVisibility), on BOTH the 200 (admin pool wired) and 501 (admin pool
// absent) paths. Guards against a future double-mint regression or a 501 path that
// skips the audit.
func TestHandleGetByID_SuperAdmin_SingleAuditRecord(t *testing.T) {
	capture := &testCaptureHandler{}
	slogcapture.InstallDefault(t, slog.New(capture))

	// 200 path: admin pool wired, entry resolvable cross-tenant.
	e := &ledger.Entry{
		EventID: "evt-fr007-detail", EventType: "cross.tenant.detail.v1",
		ActorID: "usrA", TenantID: auditQueryTestTenant,
		Timestamp: time.Now().UTC(), Payload: []byte(`{}`),
	}
	relay := newHandlerStore(t)
	require.NoError(t, relay.Append(context.Background(), e))
	ctStore, err := ledger.NewMemCrossTenantStore(relay)
	require.NoError(t, err)
	muxWithStore := getByIDService(t, newHandlerStore(t), WithCrossTenantStore(ctStore))

	capture.records = capture.records[:0]
	w := getByID(muxWithStore, newSuperAdminCtx("sa-200"), e.ID)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.Equal(t, 1, countAuditMandatoryRecords(capture.records),
		"super-admin GetByID (200) must emit EXACTLY ONE FR-007 Error record; records=%v", capture.records)

	// 501 path: admin pool absent — FR-007 must still fire (audit of intent).
	store := newHandlerStore(t)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-fr007-501", EventType: "audit.detail.v1", ActorID: "usr",
		TenantID: auditQueryTestTenant, Timestamp: time.Now().UTC(), Payload: []byte(`{}`),
	})
	muxNoStore := getByIDService(t, store)
	capture.records = capture.records[:0]
	w = getByID(muxNoStore, newSuperAdminCtx("sa-501"), id)
	require.Equal(t, http.StatusNotImplemented, w.Code, "body=%s", w.Body.String())
	assert.Equal(t, 1, countAuditMandatoryRecords(capture.records),
		"super-admin GetByID (501, no admin pool) must STILL emit EXACTLY ONE FR-007 Error record; records=%v", capture.records)
}

func TestHandleGetByID_NonCanonicalTenant_InternalError(t *testing.T) {
	// A malformed (non-canonical) principal tenant is a server-side invariant break
	// (the JWT authenticator canonicalizes the claim): getEntry's tenant.ParseTenantID
	// fails → 500 ErrInternal (mirrors the list's TestList_NonCanonicalTenant path).
	store := newHandlerStore(t)
	id := seedAuditEntry(t, store, &ledger.Entry{
		EventID: "evt-detail-nc", EventType: "audit.detail.v1", ActorID: "usr",
		TenantID: auditQueryTestTenant, Timestamp: time.Now().UTC(), Payload: []byte(`{}`),
	})
	mux := getByIDService(t, store)
	p := &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-user", Roles: []string{"admin"},
		TenantID: "not-a-uuid", AuthMethod: "test",
	}
	ctx := withAllowAuthorizer(auth.WithPrincipal(context.Background(), p))
	w := getByID(mux, ctx, id)
	require.Equal(t, http.StatusInternalServerError, w.Code, "body=%s", w.Body.String())
}

// TestGetAdapter_Get_Unauthenticated_Typed401 exercises the adapter's
// defense-in-depth auth check directly (the route gate would 401 before the adapter
// on the mux path): no principal → KindUnauthenticated errcode → the typed-envelope
// wrapper (mapGetError) maps it to Get401ErrorResponse, NOT a raw Go error (F3,
// #2288 review). This pins the declared-status → typed-response contract.
func TestGetAdapter_Get_Unauthenticated_Typed401(t *testing.T) {
	svc, _ := newTestService()
	a := GetAdapter{S: svc}
	resp, err := a.Get(context.Background(), &auditget.Request{ID: "some-id"})
	require.NoError(t, err, "declared 401 must be a typed response, not a Go error")
	_, ok := resp.(auditget.Get401ErrorResponse)
	assert.True(t, ok, "unauthenticated GET must map to Get401ErrorResponse, got %T", resp)
}

func TestListAdapter_List_Unauthenticated_ReturnsErrcode(t *testing.T) {
	svc, _ := newTestService()
	a := ListAdapter{S: svc}

	resp, err := a.List(context.Background(), &auditlist.Request{})

	require.Nil(t, resp)
	errcodetest.AssertCode(t, err, errcode.ErrAuthUnauthorized)
}
