package sessionprojection_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/slices/sessionprojection"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	registrysummary "github.com/ghbvf/gocell/generated/contracts/http/session/registry-summary/v1"
)

// testTenantIDStr is the canonical test tenant UUID.
const testTenantIDStr = "00000000-0000-0000-0000-000000000001"

// testAdminAuthorizer is a minimal auth.Authorizer that always allows.
type testAdminAuthorizer struct{}

func (a *testAdminAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("testAdminAuthorizer: authz.Allow: " + err.Error())
	}
	return dec, nil
}

// withAllowAuthorizer wraps ctx with an allow-all authorizer.
func withAllowAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, &testAdminAuthorizer{})
}

// testDenyAuthorizer is an auth.Authorizer that denies every request, modeling
// a PDP that withholds session:read so the route-level gate must 403.
type testDenyAuthorizer struct{}

func (a *testDenyAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return authz.Deny("test: session:read denied"), nil
}

// withDenyAuthorizer wraps ctx with a deny-all authorizer.
func withDenyAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, &testDenyAuthorizer{})
}

// userCtxWithTenant returns a context carrying a tenant-scoped user principal
// (no Authorizer) — the base for route-gate cases that vary only the PDP wiring.
func userCtxWithTenant(roles ...string) context.Context {
	p := &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    "route-gate-user",
		Roles:      roles,
		TenantID:   testTenantIDStr,
		AuthMethod: "test",
	}
	return auth.WithPrincipal(context.Background(), p)
}

// adminCtx returns a context with an admin principal carrying testTenantID.
// Uses auth.WithPrincipal directly so that p.TenantID is populated (auth.TestContext
// leaves TenantID empty, which our tenant-isolation guard correctly rejects).
func adminCtx() context.Context {
	p := &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    "admin-user-1",
		Roles:      []string{"admin"},
		TenantID:   testTenantIDStr,
		AuthMethod: "test",
	}
	return withAllowAuthorizer(auth.WithPrincipal(context.Background(), p))
}

// noTenantCtx returns a context with an authenticated principal but NO TenantID —
// this is the fail-open vector that our 403 gate must catch.
func noTenantCtx() context.Context {
	// auth.TestContext does not set TenantID, so p.TenantID == "" — exactly the
	// empty-tenant vector that must be rejected with 403.
	return withAllowAuthorizer(auth.TestContext("user-1", []string{"admin"}))
}

// testEntry implements cellvocab.ProjectionEvent for seeding the service in tests.
type testEntry struct {
	eventID   string
	sessionID string
	tenantStr string
}

func (e testEntry) EventID() string       { return e.eventID }
func (e testEntry) Stream() string        { return "event.session.created.v1" }
func (e testEntry) OccurredAt() time.Time { return time.Time{} }
func (e testEntry) RestoreContext(ctx context.Context) context.Context {
	return ctxkeys.WithTenantID(ctx, e.tenantStr)
}

func (e testEntry) Payload() []byte {
	b, _ := json.Marshal(map[string]string{"sessionId": e.sessionID, "userId": "test-user"})
	return b
}

// ensure testEntry implements the interface at compile time.
var _ cellvocab.ProjectionEvent = testEntry{}

// seedSession applies a session.created event to svc for tenantStr.
func seedSession(t *testing.T, svc *sessionprojection.Service, tenantStr, sessionID, eventID string) {
	t.Helper()
	ctx := ctxkeys.WithTenantID(context.Background(), tenantStr)
	err := svc.HandleSessionCreated(ctx, testEntry{
		eventID:   eventID,
		sessionID: sessionID,
		tenantStr: tenantStr,
	})
	require.NoError(t, err, "seed session %s", sessionID)
}

// newHandlerMux builds an http.Handler that serves the registry-summary endpoint
// through the generated RegisterRoutes path. This is deliberate: RegisterRoutes
// runs auth.Mount, which wraps the handler in the RequirePermission(session:read)
// policy middleware. A bare mux.Handle of the generated handler would skip that
// wrapper (the generated ServeHTTP only calls the inner handler), so every PDP
// deny / missing-Authorizer case would silently pass — exactly the route-level
// auth surface the cases below exercise.
func newHandlerMux(t *testing.T, svc *sessionprojection.Service) http.Handler {
	t.Helper()
	policy := auth.RequirePermission(authz.PermSessionRead())
	h := registrysummary.NewHandler(sessionprojection.NewSummaryAdapter(svc), policy)
	mux := celltest.NewTestMux()
	require.NoError(t, h.RegisterRoutes(mux), "RegisterRoutes must mount the policy-wrapped handler")
	return mux
}

// doRequest performs a GET to /api/v1/access/sessions/registry-summary with ctx.
func doRequest(t *testing.T, handler http.Handler, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/sessions/registry-summary", nil)
	req = req.WithContext(ctx)
	handler.ServeHTTP(w, req)
	return w
}

// TestHandler_NoPrincipal_Returns401 verifies that a request with no auth
// principal returns HTTP 401.
func TestHandler_NoPrincipal_Returns401(t *testing.T) {
	t.Parallel()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)
	mux := newHandlerMux(t, svc)

	// No principal in context — use only the allow authorizer so PDP passes.
	ctx := withAllowAuthorizer(context.Background())
	w := doRequest(t, mux, ctx)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assertErrCode(t, w, "ERR_AUTH_UNAUTHORIZED")
}

// TestHandler_NoTenant_Returns403 verifies that an authenticated principal with
// no tenant scope is fail-closed with HTTP 403 (tenant isolation, epic #1337 PR-2a F1).
func TestHandler_NoTenant_Returns403(t *testing.T) {
	t.Parallel()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)
	mux := newHandlerMux(t, svc)

	w := doRequest(t, mux, noTenantCtx())

	assert.Equal(t, http.StatusForbidden, w.Code)
	assertErrCode(t, w, "ERR_AUTH_FORBIDDEN")
}

// TestHandler_RouteGate_NoAuthorizer_Returns403 verifies the route-level PDP
// gate fails closed when no Authorizer is wired into the request context. The
// principal is a valid tenant-scoped user, so the only thing missing is the
// PDP — proving the gate (not the service) rejects. This case is only reachable
// because newHandlerMux mounts the policy-wrapped handler via RegisterRoutes.
func TestHandler_RouteGate_NoAuthorizer_Returns403(t *testing.T) {
	t.Parallel()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)
	mux := newHandlerMux(t, svc)

	w := doRequest(t, mux, userCtxWithTenant("admin"))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assertErrCode(t, w, "ERR_AUTH_FORBIDDEN")
}

// TestHandler_RouteGate_DenyAuthorizer_Returns403 verifies the route-level PDP
// gate rejects a principal the PDP denies session:read for, even with a valid
// tenant — the deny path the previous bare-handler mount never exercised.
func TestHandler_RouteGate_DenyAuthorizer_Returns403(t *testing.T) {
	t.Parallel()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)
	mux := newHandlerMux(t, svc)

	w := doRequest(t, mux, withDenyAuthorizer(userCtxWithTenant("viewer")))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assertErrCode(t, w, "ERR_AUTH_FORBIDDEN")
}

// TestHandler_Admin_Returns200_EmptyCount verifies the normal path: admin with
// a valid tenant gets HTTP 200 with TotalSessions = 0 (no sessions yet). With
// newHandlerMux now mounting via RegisterRoutes, this also asserts the allow
// path of the route-level gate (allow Authorizer + admin → permit → 200).
func TestHandler_Admin_Returns200_EmptyCount(t *testing.T) {
	t.Parallel()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)
	mux := newHandlerMux(t, svc)

	w := doRequest(t, mux, adminCtx())

	require.Equal(t, http.StatusOK, w.Code)
	var resp registrysummary.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data)
	assert.Equal(t, int64(0), resp.Data.TotalSessions)
}

// TestHandler_Returns200_WithCount verifies that sessions added to the read
// model are reflected in the count response for the caller's tenant.
func TestHandler_Returns200_WithCount(t *testing.T) {
	t.Parallel()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)

	seedSession(t, svc, testTenantIDStr, "sess-A", "e1")
	seedSession(t, svc, testTenantIDStr, "sess-B", "e2")

	mux := newHandlerMux(t, svc)
	w := doRequest(t, mux, adminCtx())

	require.Equal(t, http.StatusOK, w.Code)
	var resp registrysummary.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data)
	assert.Equal(t, int64(2), resp.Data.TotalSessions)
}

// TestHandler_NoSessionIDsInResponse verifies that raw session IDs are never
// returned on the wire (privacy boundary: only the count is exposed).
func TestHandler_NoSessionIDsInResponse(t *testing.T) {
	t.Parallel()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)

	seedSession(t, svc, testTenantIDStr, "secret-session-id-XYZ", "e-priv")

	mux := newHandlerMux(t, svc)
	w := doRequest(t, mux, adminCtx())

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, "secret-session-id-XYZ",
		"raw session IDs must never appear in the response body")
	assert.NotContains(t, body, "sessionId",
		"sessionId field must not appear in the response")
}

// TestHandler_TenantIsolation verifies that one tenant's session count is not
// visible to another tenant's principal (cross-tenant isolation).
func TestHandler_TenantIsolation(t *testing.T) {
	t.Parallel()
	otherTenantStr := "eeeeeeee-0000-0000-0000-000000000099"
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)

	// Seed 1 session for the caller's tenant and 2 for another tenant.
	seedSession(t, svc, testTenantIDStr, "sess-mine", "e-own")
	seedSession(t, svc, otherTenantStr, "sess-other-1", "e-oth1")
	seedSession(t, svc, otherTenantStr, "sess-other-2", "e-oth2")

	mux := newHandlerMux(t, svc)
	// adminCtx() carries testTenantIDStr — should see only 1, not 3.
	w := doRequest(t, mux, adminCtx())

	require.Equal(t, http.StatusOK, w.Code)
	var resp registrysummary.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data)
	assert.Equal(t, int64(1), resp.Data.TotalSessions,
		"caller should only see sessions for their own tenant (isolation)")
}

// TestHandler_NonCanonicalTenant_Returns500 verifies that a principal with a
// non-empty but non-canonical TenantID (e.g. "not-a-uuid") causes a 500 Internal
// Server Error. The JWT authenticator must canonicalise the tenant; if it does not,
// tenant.ParseTenantID fails in the handler, which is an internal invariant break
// (not a client error), so the framework returns 500 + ERR_INTERNAL.
func TestHandler_NonCanonicalTenant_Returns500(t *testing.T) {
	t.Parallel()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)
	mux := newHandlerMux(t, svc)

	// Build a principal with a non-empty but non-canonical TenantID.
	p := &auth.Principal{
		Kind:       auth.PrincipalUser,
		Subject:    "admin-user-1",
		Roles:      []string{"admin"},
		TenantID:   "not-a-uuid",
		AuthMethod: "test",
	}
	ctx := withAllowAuthorizer(auth.WithPrincipal(context.Background(), p))
	w := doRequest(t, mux, ctx)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assertErrCode(t, w, "ERR_INTERNAL")
}

// assertErrCode checks that the response body contains an error with the expected code.
func assertErrCode(t *testing.T, w *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "body: %s", w.Body.String())
	assert.Equal(t, wantCode, body.Error.Code)
}
