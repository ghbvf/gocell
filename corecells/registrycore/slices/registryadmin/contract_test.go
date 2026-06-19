package registryadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/mem"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

const (
	approveContractID = "http.registry.contract.approve.v1"
	rejectContractID  = "http.registry.contract.reject.v1"
	retireContractID  = "http.registry.contract.retire.v1"
	testTenantStr     = "00000000-0000-0000-0000-000000000001"
	seedID            = "http.example.foo.v1"
)

var (
	testTenant = tenant.TenantID(testTenantStr)
	testEpoch  = mustTime("2026-06-18T00:00:00Z")
)

func mustTime(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustTime: " + err.Error())
	}
	return ts
}

// mockAuthorizer is a test-only auth.Authorizer returning a fixed Decision
// (mock placement per go-standards.md §Naming — same-package test file).
type mockAuthorizer struct {
	decision authz.Decision
	err      error
}

func (m *mockAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return m.decision, m.err
}

func allowAuthorizer() *mockAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("allowAuthorizer: " + err.Error())
	}
	return &mockAuthorizer{decision: dec}
}

func denyAuthorizer(reason string) *mockAuthorizer {
	return &mockAuthorizer{decision: authz.Deny(reason)}
}

// noopTxRunner executes fn directly without a real transaction (test double).
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = noopTxRunner{}

// adminResolver mirrors the cellHTTPResolver for the three admin contracts so
// RegisterRoutes installs the same contract-derived PDP gate production uses.
var adminResolver = auth.NewStaticMethodPolicyResolver(map[string]string{
	approveContractID: "registry:approve",
	rejectContractID:  "registry:reject",
	retireContractID:  "registry:retire",
})

// newAdminMux mounts the three admin handlers over the given store under the
// production-mirroring prefix /api/v1/registry. The returned mux shares one
// Service over the store, so transitions hit the real kernel state machine.
func newAdminMux(t *testing.T, store ports.Registry) http.Handler {
	t.Helper()
	svc, err := NewService(store, WithTxManager(persistence.WrapForCell(noopTxRunner{})))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := NewHandler(svc, adminResolver)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/registry", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	return mux
}

// adminCtx returns a context with an admin principal, the test tenant, and the
// given authorizer injected. Pass nil to omit the authorizer.
func adminCtx(authorizer auth.Authorizer) context.Context {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = ctxkeys.WithTenantID(ctx, testTenantStr)
	if authorizer != nil {
		ctx = auth.WithAuthorizer(ctx, authorizer)
	}
	return ctx
}

func postAdmin(t *testing.T, mux http.Handler, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

// seedTo creates a fresh registration (id = seedID) and advances it along the
// legal lifecycle path (submitted → probing → conformant → pending-approval →
// approved → active) up to and including target, so a test can drive an endpoint
// against a known state. The kernel registry.Transition enforces legality; seedTo
// only walks the canonical forward path.
func seedTo(t *testing.T, store ports.Registry, target registry.RegistrationState) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Create(ctx, testTenant, registry.SubmitInput{
		ID: seedID, Kind: "http", Submitter: "submitter-1",
	}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	if target == registry.StateSubmitted() {
		return
	}
	for _, st := range []registry.RegistrationState{
		registry.StateProbing(),
		registry.StateConformant(),
		registry.StatePendingApproval(),
		registry.StateApproved(),
		registry.StateActive(),
	} {
		if _, err := store.Transition(ctx, testTenant, registry.AdvanceInput{
			ID: seedID, To: st, Actor: "system",
		}); err != nil {
			t.Fatalf("seed Transition to %s: %v", st, err)
		}
		if st == target {
			return
		}
	}
	t.Fatalf("seedTo: unreachable target state %s", target)
}

// decodeData extracts data.state and data.approver from a 200 admin response body.
func decodeData(t *testing.T, body []byte) (state, approver string) {
	t.Helper()
	var resp struct {
		Data struct {
			State    string `json:"state"`
			Approver string `json:"approver"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, body)
	}
	return resp.Data.State, resp.Data.Approver
}

func approvePath(id string) string { return "/api/v1/registry/contracts/" + id + "/approve" }
func rejectPath(id string) string  { return "/api/v1/registry/contracts/" + id + "/reject" }
func retirePath(id string) string  { return "/api/v1/registry/contracts/" + id + "/retire" }

// --- approve ---------------------------------------------------------------

// TestContractApproveServe_RequestSchema validates the optional-reason body
// schema (positive + additionalProperties:false negative) independent of routing.
func TestContractApproveServe_RequestSchema(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), approveContractID)
	c.ValidateRequest(t, []byte(`{}`))
	c.ValidateRequest(t, []byte(`{"reason":"looks good"}`))
	c.MustRejectRequest(t, []byte(`{"extra":"nope"}`)) // additionalProperties:false
}

// TestContractApproveServe_OK: admin (allow PDP) approving a pending-approval
// registration gets 200 with state=approved and a schema-valid body.
func TestContractApproveServe_OK(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), approveContractID)
	clk := clockmock.New(testEpoch)
	store := mem.NewRegistry(clk)
	seedTo(t, store, registry.StatePendingApproval())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), approvePath(seedID), `{"reason":"ok"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
	state, approver := decodeData(t, rec.Body.Bytes())
	if state != registry.StateApproved().String() {
		t.Fatalf("state = %q, want approved", state)
	}
	// Approver attribution: the authenticated admin subject (adminCtx → "admin-1"),
	// not the seed actor ("system"). This is the core audit property of approve.
	if approver != "admin-1" {
		t.Fatalf("approver = %q, want admin-1 (authenticated admin subject)", approver)
	}
}

// TestContractApproveServe_Unauthenticated: no principal ⇒ the route gate 401s.
func TestContractApproveServe_Unauthenticated(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), approveContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StatePendingApproval())

	rec := postAdmin(t, newAdminMux(t, store), context.Background(), approvePath(seedID), `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractApproveServe_Forbidden: a non-admin (PDP deny) is 403 and the state
// machine is never touched (fail-closed before the transition).
func TestContractApproveServe_Forbidden(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), approveContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StatePendingApproval())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(denyAuthorizer("no registry:approve")), approvePath(seedID), `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())

	// State untouched: still pending-approval (gate fail-closed before transition).
	got, ok, err := store.Get(context.Background(), testTenant, seedID)
	if err != nil || !ok {
		t.Fatalf("Get after deny: ok=%v err=%v", ok, err)
	}
	if got.State != registry.StatePendingApproval() {
		t.Fatalf("state after deny = %s, want pending-approval (untouched)", got.State)
	}
}

// TestContractApproveServe_NotFound: approving an unknown id ⇒ 404.
func TestContractApproveServe_NotFound(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), approveContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), approvePath("http.missing.v1"), `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractApproveServe_InvalidTransition: approving a submitted (not
// pending-approval) registration ⇒ 400 (kernel rejects the illegal transition;
// ErrRegistrationInvalidTransition is KindInvalid → 400, mirrors saga.Transition).
func TestContractApproveServe_InvalidTransition(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), approveContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StateSubmitted())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), approvePath(seedID), `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// --- reject ----------------------------------------------------------------

// TestContractRejectServe_OK: admin rejecting a pending-approval registration gets
// 200 with state=rejected (a terminal state).
func TestContractRejectServe_OK(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), rejectContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StatePendingApproval())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), rejectPath(seedID), `{"reason":"fails policy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
	if state, _ := decodeData(t, rec.Body.Bytes()); state != registry.StateRejected().String() {
		t.Fatalf("state = %q, want rejected", state)
	}
}

// TestContractRejectServe_Forbidden: PDP deny ⇒ 403.
func TestContractRejectServe_Forbidden(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), rejectContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StatePendingApproval())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(denyAuthorizer("no registry:reject")), rejectPath(seedID), `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractRejectServe_Unauthenticated: no principal ⇒ the route gate 401s.
func TestContractRejectServe_Unauthenticated(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), rejectContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StatePendingApproval())

	rec := postAdmin(t, newAdminMux(t, store), context.Background(), rejectPath(seedID), `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractRejectServe_NotFound: rejecting an unknown id ⇒ 404.
func TestContractRejectServe_NotFound(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), rejectContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), rejectPath("http.missing.v1"), `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractRejectServe_InvalidTransition: rejecting an active (past the
// probing/pending-approval reject window) registration ⇒ 400 (illegal transition).
func TestContractRejectServe_InvalidTransition(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), rejectContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StateActive())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), rejectPath(seedID), `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// --- retire ----------------------------------------------------------------

// TestContractRetireServe_OK: admin retiring an active registration gets 200 with
// state=retired (active → retired terminal transition).
func TestContractRetireServe_OK(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), retireContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StateActive())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), retirePath(seedID), `{"reason":"superseded"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
	if state, _ := decodeData(t, rec.Body.Bytes()); state != registry.StateRetired().String() {
		t.Fatalf("state = %q, want retired", state)
	}
}

// TestContractRetireServe_Forbidden: PDP deny ⇒ 403.
func TestContractRetireServe_Forbidden(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), retireContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StateActive())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(denyAuthorizer("no registry:retire")), retirePath(seedID), `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractRetireServe_Unauthenticated: no principal ⇒ the route gate 401s.
func TestContractRetireServe_Unauthenticated(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), retireContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StateActive())

	rec := postAdmin(t, newAdminMux(t, store), context.Background(), retirePath(seedID), `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractRetireServe_NotFound: retiring an unknown id ⇒ 404.
func TestContractRetireServe_NotFound(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), retireContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), retirePath("http.missing.v1"), `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractRetireServe_InvalidTransition: retiring a pending-approval (not
// active) registration ⇒ 400 (retire requires active; illegal transition).
func TestContractRetireServe_InvalidTransition(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), retireContractID)
	store := mem.NewRegistry(clockmock.New(testEpoch))
	seedTo(t, store, registry.StatePendingApproval())

	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), retirePath(seedID), `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}
