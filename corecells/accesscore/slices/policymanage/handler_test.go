package policymanage

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/internal/testoutbox"
	policyCreate "github.com/ghbvf/gocell/generated/contracts/http/policy/create/v1"
	policyDelete "github.com/ghbvf/gocell/generated/contracts/http/policy/delete/v1"
	policyGet "github.com/ghbvf/gocell/generated/contracts/http/policy/get/v1"
	policyUpdate "github.com/ghbvf/gocell/generated/contracts/http/policy/update/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

const (
	testHandlerTenantStr    = "20000000-0000-0000-0000-000000000001"
	testHandlerAdminSubject = "handler-admin"
)

// The cell-level RouteGroup mounts policy slice at /api/v1/access/policies.
const policiesPrefix = "/api/v1/access/policies"

// fixedAuthorizer is a local test-only auth.Authorizer that returns a fixed
// Decision. It cannot import accesscoretest (cycle: accesscoretest imports
// corecells/accesscore which imports this slice), so the minimal type is defined
// here. Verdict construction (authz.Allow/Deny) lives in _test.go per
// AUTHZ-DECISION-ALLOW-DENY-CALLER-01.
type fixedAuthorizer struct {
	decision authz.Decision
}

func (f *fixedAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return f.decision, nil
}

func allowAuthorizer() *fixedAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("test allowAuthorizer: authz.Allow: " + err.Error())
	}
	return &fixedAuthorizer{decision: dec}
}

func withAllowAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, allowAuthorizer())
}

func withDenyAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, &fixedAuthorizer{decision: authz.Deny("test: denied")})
}

// withHandlerAdmin injects an admin principal, tenant, and an allow Authorizer
// into the request context so it satisfies auth.RequirePermission PDP gate.
func withHandlerAdmin(req *http.Request) *http.Request {
	ctx := withAllowAuthorizer(ctxkeys.WithTenantID(
		auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin}),
		testHandlerTenantStr,
	))
	return req.WithContext(ctx)
}

// withHandlerAdminNoTenant injects admin principal and allow Authorizer but no
// TenantID — used to test the missing-tenant → 403 path (PDP passes, tenant
// check in service fails).
func withHandlerAdminNoTenant(req *http.Request) *http.Request {
	return req.WithContext(withAllowAuthorizer(auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin})))
}

type stubPolicyTxRunner struct{}

func (s *stubPolicyTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// setupPolicyHandler returns an http.Handler backed by in-memory repos and a
// noop outbox writer, mounted at the cell-level prefix.
func setupPolicyHandler(t testing.TB) http.Handler {
	t.Helper()
	repo := mem.NewPolicyRepository()
	ow := &recordingWriter{}
	svc, err := NewService(
		clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, ow))),
		WithTxManager(persistence.WrapForCell(&stubPolicyTxRunner{})),
	)
	require.NoError(t, err)
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route(policiesPrefix, func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	return mux
}

// minimalCreateBody returns a valid JSON body for POST /api/v1/access/policies.
const minimalCreateBody = `{"name":"TestPolicy","rules":[{"id":"r1","name":"Allow all","effect":"allow"}]}`

// minimalUpdateBody returns a valid JSON body for PUT /api/v1/access/policies/{id}.
const minimalUpdateBody = `{"name":"Updated","rules":[{"id":"r1","name":"Allow all","effect":"allow"}],"expectedVersion":1}`

// --- Create tests ---

func TestHandler_Create_OK(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(minimalCreateBody))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "TestPolicy")

	var resp struct {
		Data policyCreate.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.ID)
	assert.Equal(t, "TestPolicy", resp.Data.Name)
	assert.Equal(t, int64(1), resp.Data.Version)
}

func TestHandler_Create_BadJSON(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_Create_MissingName(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	body := `{"rules":[{"id":"r1","name":"Allow all","effect":"allow"}]}`
	req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	// schema requires "name" → 400
	assert.Equal(t, http.StatusBadRequest, w.Code)
	errcodetest.AssertWireCode(t, w, http.StatusBadRequest, errcode.ErrValidationFailed)
}

func TestHandler_Create_MissingRules(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	body := `{"name":"P"}`
	req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_Create_UnknownField(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	body := `{"name":"P","rules":[{"id":"r1","name":"N","effect":"allow"}],"unknown":1}`
	req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Auth tests: Create ---

func TestHandler_Create_Authz(t *testing.T) {
	cases := []struct {
		name       string
		setupCtx   func(*http.Request) *http.Request
		wantStatus int
		wantCode   errcode.Code
	}{
		{
			name:       "no_auth",
			setupCtx:   func(r *http.Request) *http.Request { return r },
			wantStatus: http.StatusUnauthorized,
			wantCode:   errcode.ErrAuthUnauthorized,
		},
		{
			name: "non_admin",
			setupCtx: func(r *http.Request) *http.Request {
				// Viewer with a deny Authorizer → PDP denies → 403.
				return r.WithContext(withDenyAuthorizer(ctxkeys.WithTenantID(
					auth.TestContext("viewer-1", []string{"viewer"}),
					testHandlerTenantStr,
				)))
			},
			wantStatus: http.StatusForbidden,
			wantCode:   errcode.ErrAuthForbidden,
		},
		{
			name:       "admin_ok",
			setupCtx:   withHandlerAdmin,
			wantStatus: http.StatusCreated,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := setupPolicyHandler(t)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(minimalCreateBody))
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(w, tc.setupCtx(req))
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCode != "" {
				errcodetest.AssertWireCode(t, w, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

func TestHandler_Create_MissingTenant_403(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(minimalCreateBody))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdminNoTenant(req))

	// No tenant → service returns KindPermissionDenied → mapCreateError → 403.
	assert.Equal(t, http.StatusForbidden, w.Code)
	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_Update_MissingTenant_403(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, policiesPrefix+"/pol-x", strings.NewReader(minimalUpdateBody))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdminNoTenant(req))

	assert.Equal(t, http.StatusForbidden, w.Code)
	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_Delete_MissingTenant_403(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, policiesPrefix+"/pol-x?expectedVersion=1", nil)
	handler.ServeHTTP(w, withHandlerAdminNoTenant(req))

	assert.Equal(t, http.StatusForbidden, w.Code)
	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_Get_MissingTenant_403(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, policiesPrefix+"/pol-x", nil)
	handler.ServeHTTP(w, withHandlerAdminNoTenant(req))

	assert.Equal(t, http.StatusForbidden, w.Code)
	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

func TestHandler_List_MissingTenant_403(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, policiesPrefix, nil)
	handler.ServeHTTP(w, withHandlerAdminNoTenant(req))

	assert.Equal(t, http.StatusForbidden, w.Code)
	errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// --- Get tests ---

func TestHandler_Get_OK(t *testing.T) {
	handler := setupPolicyHandler(t)

	// Create a policy first via POST.
	cw := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(minimalCreateBody))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(cw, withHandlerAdmin(req))
	require.Equal(t, http.StatusCreated, cw.Code)

	var created struct {
		Data policyCreate.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &created))
	policyID := created.Data.ID

	// GET by ID.
	w := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, policiesPrefix+"/"+policyID, nil)
	handler.ServeHTTP(w, withHandlerAdmin(getReq))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), policyID)
}

func TestHandler_Get_NotFound(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, policiesPrefix+"/pol-nonexistent", nil)
	handler.ServeHTTP(w, withHandlerAdmin(req))

	errcodetest.AssertWireCode(t, w, http.StatusNotFound, errcode.ErrAuthPolicyNotFound)
}

func TestHandler_Get_EmptyID(t *testing.T) {
	// Direct handler test: the generated get handler enforces minLength:1 on the
	// {id} path parameter. Send a request with no path value to trigger the guard.
	getH := policyGet.NewHandler(
		GetAdapter{s: newServiceForAdapterTest(t)},
		auth.RequirePermission(authz.PermPolicyRead()),
	)
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/access/policies/", nil)

	gw := httptest.NewRecorder()
	getH.ServeHTTP(gw, withHandlerAdmin(getReq))
	// id="" has len 0 → generated guard returns 400.
	assert.Equal(t, http.StatusBadRequest, gw.Code)
}

// --- Update tests ---

func TestHandler_Update_OK(t *testing.T) {
	handler := setupPolicyHandler(t)

	// Create.
	cw := httptest.NewRecorder()
	cr := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(minimalCreateBody))
	cr.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(cw, withHandlerAdmin(cr))
	require.Equal(t, http.StatusCreated, cw.Code)
	var created struct {
		Data policyCreate.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &created))
	policyID := created.Data.ID

	// Update.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, policiesPrefix+"/"+policyID, strings.NewReader(minimalUpdateBody))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Updated")
}

func TestHandler_Update_NotFound(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, policiesPrefix+"/pol-ghost", strings.NewReader(minimalUpdateBody))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	errcodetest.AssertWireCode(t, w, http.StatusNotFound, errcode.ErrAuthPolicyNotFound)
}

func TestHandler_Update_VersionConflict(t *testing.T) {
	handler := setupPolicyHandler(t)

	// Create first.
	cw := httptest.NewRecorder()
	cr := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(minimalCreateBody))
	cr.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(cw, withHandlerAdmin(cr))
	require.Equal(t, http.StatusCreated, cw.Code)
	var created struct {
		Data policyCreate.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &created))
	policyID := created.Data.ID

	// Update with wrong version (99 instead of 1).
	body := `{"name":"Updated","rules":[{"id":"r1","name":"Allow all","effect":"allow"}],"expectedVersion":99}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, policiesPrefix+"/"+policyID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	errcodetest.AssertWireCode(t, w, http.StatusConflict, errcode.ErrVersionConflict)
}

func TestHandler_Update_BadJSON(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, policiesPrefix+"/pol-x", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Delete tests ---

func TestHandler_Delete_OK(t *testing.T) {
	handler := setupPolicyHandler(t)

	// Create.
	cw := httptest.NewRecorder()
	cr := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(minimalCreateBody))
	cr.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(cw, withHandlerAdmin(cr))
	require.Equal(t, http.StatusCreated, cw.Code)
	var created struct {
		Data policyCreate.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &created))
	policyID := created.Data.ID

	// Delete.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, policiesPrefix+"/"+policyID+"?expectedVersion=1", nil)
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandler_Delete_NotFound(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, policiesPrefix+"/pol-ghost?expectedVersion=1", nil)
	handler.ServeHTTP(w, withHandlerAdmin(req))

	errcodetest.AssertWireCode(t, w, http.StatusNotFound, errcode.ErrAuthPolicyNotFound)
}

func TestHandler_Delete_VersionConflict(t *testing.T) {
	handler := setupPolicyHandler(t)

	// Create.
	cw := httptest.NewRecorder()
	cr := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(minimalCreateBody))
	cr.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(cw, withHandlerAdmin(cr))
	require.Equal(t, http.StatusCreated, cw.Code)
	var created struct {
		Data policyCreate.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &created))
	policyID := created.Data.ID

	// Delete with wrong version.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, policiesPrefix+"/"+policyID+"?expectedVersion=99", nil)
	handler.ServeHTTP(w, withHandlerAdmin(req))

	errcodetest.AssertWireCode(t, w, http.StatusConflict, errcode.ErrVersionConflict)
}

func TestHandler_Delete_MissingExpectedVersion(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, policiesPrefix+"/pol-x", nil) // no ?expectedVersion
	handler.ServeHTTP(w, withHandlerAdmin(req))

	// Generated handler rejects missing expectedVersion with 400.
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- List tests ---

func TestHandler_List_Empty(t *testing.T) {
	handler := setupPolicyHandler(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, policiesPrefix, nil)
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data    []any `json:"data"`
		HasMore bool  `json:"hasMore"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data)
	assert.False(t, resp.HasMore)
}

func TestHandler_List_WithItems(t *testing.T) {
	handler := setupPolicyHandler(t)

	// Create two policies.
	for i := range 2 {
		body := `{"name":"P` + string(rune('0'+i)) + `","rules":[{"id":"r1","name":"Allow all","effect":"allow"}]}`
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(w, withHandlerAdmin(req))
		require.Equal(t, http.StatusCreated, w.Code)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, policiesPrefix+"?limit=10", nil)
	handler.ServeHTTP(w, withHandlerAdmin(req))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data, 2)
}

// --- Typed adapter unit tests ---

// newServiceForAdapterTest builds a minimal Service for adapter-level unit tests.
func newServiceForAdapterTest(t testing.TB) *Service {
	t.Helper()
	repo := mem.NewPolicyRepository()
	svc, err := NewService(
		clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithTxManager(persistence.WrapForCell(&stubPolicyTxRunner{})),
	)
	require.NoError(t, err)
	return svc
}

func TestCreateAdapter_HappyPath_Returns201(t *testing.T) {
	repo := mem.NewPolicyRepository()
	svc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithTxManager(persistence.WrapForCell(&stubPolicyTxRunner{})))
	require.NoError(t, err)
	ad := CreateAdapter{s: svc}

	body := &policyCreate.Request{
		Name:  "P",
		Rules: []*policyCreate.RequestRulesItem{{ID: "r1", Name: "N", Effect: "allow"}},
	}
	ctx := ctxkeys.WithTenantID(auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr)
	resp, err := ad.Create(ctx, body)
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create201JSONResponse)
	assert.True(t, ok, "expected Create201JSONResponse, got %T", resp)
}

func TestUpdateAdapter_NotFound_Returns404Typed(t *testing.T) {
	ad := UpdateAdapter{s: newServiceForAdapterTest(t)}
	ctx := ctxkeys.WithTenantID(auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr)
	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:              "pol-ghost",
		Name:            "X",
		Rules:           []*policyUpdate.RequestRulesItem{{ID: "r1", Name: "N", Effect: "allow"}},
		ExpectedVersion: 1,
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update404ErrorResponse)
	assert.True(t, ok, "expected Update404ErrorResponse, got %T", resp)
}

func TestUpdateAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	svc := newServiceForAdapterTest(t)
	// Create a policy to get a real ID.
	ctx := ctxkeys.WithTenantID(auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr)
	p, err := svc.Create(ctx, CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	ad := UpdateAdapter{s: svc}
	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:              p.ID,
		Name:            "X",
		Rules:           []*policyUpdate.RequestRulesItem{{ID: "r1", Name: "N", Effect: "allow"}},
		ExpectedVersion: 99, // wrong version
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update409ErrorResponse)
	assert.True(t, ok, "expected Update409ErrorResponse, got %T", resp)
}

func TestDeleteAdapter_NotFound_Returns404Typed(t *testing.T) {
	ad := DeleteAdapter{s: newServiceForAdapterTest(t)}
	ctx := ctxkeys.WithTenantID(auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr)
	resp, err := ad.Delete(ctx, &policyDelete.Request{ID: "pol-ghost", ExpectedVersion: 1})
	require.NoError(t, err)
	_, ok := resp.(policyDelete.Delete404ErrorResponse)
	assert.True(t, ok, "expected Delete404ErrorResponse, got %T", resp)
}

func TestDeleteAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	svc := newServiceForAdapterTest(t)
	ctx := ctxkeys.WithTenantID(auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr)
	p, err := svc.Create(ctx, CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	ad := DeleteAdapter{s: svc}
	resp, err := ad.Delete(ctx, &policyDelete.Request{ID: p.ID, ExpectedVersion: 99})
	require.NoError(t, err)
	_, ok := resp.(policyDelete.Delete409ErrorResponse)
	assert.True(t, ok, "expected Delete409ErrorResponse, got %T", resp)
}

// --- Validation error tests (#12) ---
// The generated schema validator catches structurally-invalid values (null items,
// wrong-length enum strings) at 400 before the converter runs. The converter nil
// guard and KindInvalid → 422 mapping provide defense-in-depth for any case the
// schema misses, verified here via the adapter layer to bypass the schema validator.

func TestCreateAdapter_NullRule_Returns422(t *testing.T) {
	// Bypass the schema validator and call the adapter directly with a nil rule
	// to exercise the converter nil-guard → KindInvalid → Create422ErrorResponse path.
	svc := newServiceForAdapterTest(t)
	ad := CreateAdapter{s: svc}
	ctx := ctxkeys.WithTenantID(auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr)

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name:  "P",
		Rules: []*policyCreate.RequestRulesItem{nil}, // nil rule item
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create422ErrorResponse)
	assert.True(t, ok, "expected Create422ErrorResponse for null rule item, got %T", resp)
}

func TestUpdateAdapter_NullRule_Returns422(t *testing.T) {
	svc := newServiceForAdapterTest(t)
	ctx := ctxkeys.WithTenantID(auth.TestContext(testHandlerAdminSubject, []string{auth.RoleAdmin}), testHandlerTenantStr)
	p, err := svc.Create(ctx, CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	ad := UpdateAdapter{s: svc}
	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:              p.ID,
		Name:            "P",
		Rules:           []*policyUpdate.RequestRulesItem{nil}, // nil rule item
		ExpectedVersion: 1,
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update422ErrorResponse)
	assert.True(t, ok, "expected Update422ErrorResponse for null rule item, got %T", resp)
}

func TestHandler_Create_NullRuleItem_400(t *testing.T) {
	// The generated schema validator catches null rule items at 400.
	handler := setupPolicyHandler(t)
	body := `{"name":"P","rules":[null]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, policiesPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, withHandlerAdmin(req))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	errcodetest.AssertWireCode(t, w, http.StatusBadRequest, errcode.ErrValidationFailed)
}
