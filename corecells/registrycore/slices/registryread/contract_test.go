package registryread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/mem"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

const contractID = "http.registry.contract.list.v1"

const testTenantStr = "00000000-0000-0000-0000-000000000001"

var testEpoch = mustTime("2026-06-18T00:00:00Z")

func mustTime(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustTime: " + err.Error())
	}
	return ts
}

// mockAuthorizer is a test-only auth.Authorizer returning a fixed Decision.
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

// contractListResolver mirrors the cellHTTPResolver for registryread contract
// tests: the contract-derived resolver maps the list contract to registry:read so
// RegisterRoutes installs the same PDP gate production uses (#2205, 303-US7).
var contractListResolver = auth.NewStaticMethodPolicyResolver(map[string]string{
	contractID: "registry:read",
})

// newMuxOver mounts the list handler over the given store under the
// production-mirroring prefix /api/v1/registry. RegisterRoutes installs the
// registry:read RequirePermission policy.
func newMuxOver(t *testing.T, store ports.Registry) http.Handler {
	t.Helper()
	c, err := query.NewCursorCodec(devCursorKey)
	if err != nil {
		t.Fatalf("NewCursorCodec: %v", err)
	}
	svc, err := NewService(store, outbox.DemoCellTxManager(), c, query.RunModeDemo, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := NewHandler(svc, contractListResolver)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/registry", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	return mux
}

func emptyStore() *mem.Registry {
	return mem.NewRegistry(clockmock.New(testEpoch))
}

// seededStore returns a *mem.Registry populated with two registrations:
//   - "http.example.minimal.v1": submitted only (approver=null, payloadSchema=null)
//   - "http.example.full.v1": fully populated — payloadSchema set at submit,
//     advanced through the full lifecycle to approved (approver non-null)
//
// This exercises both branches of the nullable-required fields (approver /
// payloadSchema) so ValidateHTTPResponseRecorder / assertResponseMatchesSchema
// validates the per-item schema against non-empty data instead of an empty array.
func seededStore(t *testing.T) *mem.Registry {
	t.Helper()
	ctx := context.Background()
	tnt, err := tenant.ParseTenantID(testTenantStr)
	if err != nil {
		t.Fatalf("seededStore: parse tenant: %v", err)
	}
	store := mem.NewRegistry(clockmock.New(testEpoch))

	// Item 1: minimal — no payloadSchema, stays in submitted state (both nullable
	// fields remain null in the wire response).
	if _, err := store.Create(ctx, tnt, registry.SubmitInput{
		ID:        "http.example.minimal.v1",
		Kind:      "http",
		Submitter: "cell-a",
	}); err != nil {
		t.Fatalf("seededStore: create minimal: %v", err)
	}

	// Item 2: fully-populated — payloadSchema set at submit, advanced to approved so
	// approver is non-null (both nullable fields are non-null in the wire response).
	if _, err := store.Create(ctx, tnt, registry.SubmitInput{
		ID:            "http.example.full.v1",
		Kind:          "http",
		PayloadSchema: `{"type":"object"}`,
		Submitter:     "cell-b",
	}); err != nil {
		t.Fatalf("seededStore: create full: %v", err)
	}
	steps := []registry.RegistrationState{
		registry.StateProbing(),
		registry.StateConformant(),
		registry.StatePendingApproval(),
		registry.StateApproved(),
	}
	for _, st := range steps {
		if _, err := store.Transition(ctx, tnt, registry.AdvanceInput{
			ID:    "http.example.full.v1",
			To:    st,
			Actor: "system",
		}); err != nil {
			t.Fatalf("seededStore: advance to %v: %v", st, err)
		}
	}
	return store
}

// seedApproved returns a *mem.Registry holding a single registration advanced to
// the approved state, with payloadSchema = schemaRef and approver = the
// approve-transition actor (registrar records in.Actor as Approver on approve).
// Used by TestContractListServe_OK_WithApprovedItem to assert the now-required
// approver/payloadSchema columns flow to the wire with their real values.
func seedApproved(t *testing.T, id, submitter, approver, schemaRef string) *mem.Registry {
	t.Helper()
	ctx := context.Background()
	tnt, err := tenant.ParseTenantID(testTenantStr)
	if err != nil {
		t.Fatalf("seedApproved: parse tenant: %v", err)
	}
	store := mem.NewRegistry(clockmock.New(testEpoch))
	if _, err := store.Create(ctx, tnt, registry.SubmitInput{
		ID:            id,
		Kind:          "http",
		PayloadSchema: schemaRef,
		Submitter:     submitter,
	}); err != nil {
		t.Fatalf("seedApproved: create: %v", err)
	}
	steps := []struct {
		to    registry.RegistrationState
		actor string
	}{
		{registry.StateProbing(), "system"},
		{registry.StateConformant(), "system"},
		{registry.StatePendingApproval(), "system"},
		{registry.StateApproved(), approver}, // approve actor → Approver column
	}
	for _, s := range steps {
		if _, err := store.Transition(ctx, tnt, registry.AdvanceInput{
			ID:    id,
			To:    s.to,
			Actor: s.actor,
		}); err != nil {
			t.Fatalf("seedApproved: advance to %v: %v", s.to, err)
		}
	}
	return store
}

// adminCtx returns a context with an admin principal, the test tenant, and the
// given authorizer wired. Both tenant and authorizer are required for the list
// service to reach the store successfully.
func adminCtx(authorizer auth.Authorizer) context.Context {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = ctxkeys.WithTenantID(ctx, testTenantStr)
	return auth.WithAuthorizer(ctx, authorizer)
}

func getList(t *testing.T, mux http.Handler, ctx context.Context, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	path := "/api/v1/registry/contracts"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	mux.ServeHTTP(rec, req)
	return rec
}

// TestContractListServe_QuerySchema covers every declared query parameter with a
// positive and ≥1 negative case (CONTRACT-PATH-QUERY-COVERAGE-01).
func TestContractListServe_QuerySchema(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	// limit: integer 1..500
	c.ValidateQueryParam(t, "limit", "1")
	c.ValidateQueryParam(t, "limit", "500")
	c.MustRejectQueryParam(t, "limit", "0")   // below minimum
	c.MustRejectQueryParam(t, "limit", "501") // above maximum
	c.MustRejectQueryParam(t, "limit", "abc") // wrong type
	// cursor: string maxLength 4096
	c.ValidateQueryParam(t, "cursor", "http.example.foo.v1")
	c.MustRejectQueryParam(t, "cursor", strings.Repeat("x", 4097)) // over maxLength
	// state: optional string maxLength 32. The closed value set (submitted…retired)
	// is enforced in the registryread service via registry.ParseState — NOT a
	// queryParam enum (metadata.ParamSchema models no enum) — so schema-level
	// coverage is the maxLength bound; service-level invalid-state → 400 is covered
	// by TestList_StateFilter_InvalidState.
	c.ValidateQueryParam(t, "state", "active")
	c.MustRejectQueryParam(t, "state", strings.Repeat("s", 33)) // over maxLength 32
}

// TestContractListServe_OK: an authenticated admin (allow PDP) gets a 200 whose
// body satisfies the paginated response schema when the store has items.
// Two registrations are seeded: one minimal (approver + payloadSchema = null)
// and one fully-populated (approver + payloadSchema non-null), exercising both
// branches of the nullable-required item fields so the schema validator checks
// per-item constraints on real data rather than an empty array.
func TestContractListServe_OK(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := getList(t, newMuxOver(t, seededStore(t)), adminCtx(allowAuthorizer()), "limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestContractListServe_OK_EmptyPage: schema is also valid for the empty-data
// envelope (data=[], nextCursor="", hasMore=false). This keeps envelope-only
// coverage alongside the seeded-store test above.
func TestContractListServe_OK_EmptyPage(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := getList(t, newMuxOver(t, emptyStore()), adminCtx(allowAuthorizer()), "limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestContractListServe_OK_WithApprovedItem: an approved registration serializes
// its now-required approver/payloadSchema columns (#2401: optional→required) all
// the way through the masking funnel to a schema-valid 200 body. This is the HTTP
// contract-layer counterpart to service_test's TestList_SingleItem — it proves the
// full serialize → projection funnel → wire → schema-validate path keeps the
// required columns present and non-empty, which the empty-registrar OK test cannot.
func TestContractListServe_OK_WithApprovedItem(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	reg := seedApproved(t, "http.example.approved.v1", "cell-a", "admin-1", "schema-ref-1")
	rec := getList(t, newMuxOver(t, reg), adminCtx(allowAuthorizer()), "limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)

	var resp struct {
		Data []struct {
			ID            string `json:"id"`
			Approver      string `json:"approver"`
			PayloadSchema string `json:"payloadSchema"`
			State         string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal body: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1; body=%s", len(resp.Data), rec.Body.String())
	}
	switch it := resp.Data[0]; {
	case it.Approver != "admin-1":
		t.Errorf("approver = %q, want admin-1 (required column visible after approve)", it.Approver)
	case it.PayloadSchema != "schema-ref-1":
		t.Errorf("payloadSchema = %q, want schema-ref-1", it.PayloadSchema)
	case it.State != registry.StateApproved().String():
		t.Errorf("state = %q, want %q", it.State, registry.StateApproved().String())
	}
}

// TestContractListServe_Unauthenticated: no principal ⇒ RequirePermission 401.
func TestContractListServe_Unauthenticated(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := getList(t, newMuxOver(t, emptyStore()), context.Background(), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractListServe_Forbidden: authenticated but the PDP denies registry:read ⇒ 403.
func TestContractListServe_Forbidden(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := getList(t, newMuxOver(t, emptyStore()), adminCtx(denyAuthorizer("no registry:read")), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractListServe_BadRequest: an out-of-range limit (below the minimum 1 /
// above the maximum 500) ⇒ 400 from the handler's ParsePageParams, with a shared
// error envelope.
func TestContractListServe_BadRequest(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	for _, q := range []string{"limit=0", "limit=501"} {
		rec := getList(t, newMuxOver(t, emptyStore()), adminCtx(allowAuthorizer()), q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: status = %d, want 400; body=%s", q, rec.Code, rec.Body.String())
		}
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
	}
}

// TestContractListServe_StateFilter_Valid: a valid state query parameter returns
// 200 with a response body that satisfies the contract response schema.
func TestContractListServe_StateFilter_Valid(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	// Test every valid state enum value returns 200 (empty page is valid).
	for _, st := range []string{
		"submitted", "probing", "conformant", "pending-approval",
		"approved", "rejected", "active", "retired",
	} {
		rec := getList(t, newMuxOver(t, emptyStore()), adminCtx(allowAuthorizer()), "state="+st)
		if rec.Code != http.StatusOK {
			t.Fatalf("state=%q: status = %d, want 200; body=%s", st, rec.Code, rec.Body.String())
		}
		c.ValidateHTTPResponseRecorder(t, rec)
	}
}

// TestContractListServe_StateFilter_Invalid: an unknown state value returns 400
// from the handler's enum guard, with a shared error envelope.
func TestContractListServe_StateFilter_Invalid(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	for _, q := range []string{"state=bogus", "state=SUBMITTED", "state=unknown"} {
		rec := getList(t, newMuxOver(t, emptyStore()), adminCtx(allowAuthorizer()), q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: status = %d, want 400; body=%s", q, rec.Code, rec.Body.String())
		}
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
	}
}

// TestContractListServe_QuerySchema_State: contract schema validates state
// string field (positive only — contracttest's inline param schema validates
// type/length/format but not enum membership; enum enforcement is the handler
// guard tested by TestContractListServe_StateFilter_Invalid).
func TestContractListServe_QuerySchema_State(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	// positive: every declared enum value must pass the query param type check
	for _, valid := range []string{
		"submitted", "probing", "conformant", "pending-approval",
		"approved", "rejected", "active", "retired",
	} {
		c.ValidateQueryParam(t, "state", valid)
	}
}
