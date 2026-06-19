package registrywrite

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/mem"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/governance"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	submit "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/submit/v1"
)

const testTenantStr = "00000000-0000-0000-0000-000000000001"

func mustTime(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustTime: " + err.Error())
	}
	return ts
}

// noopTxRunner executes fn directly without a real transaction (test double).
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = noopTxRunner{}

// newService builds a Service backed by the in-memory store + real gate + noop tx.
func newService(t *testing.T) *Service {
	t.Helper()
	clk := clockmock.New(testEpoch)
	store := mem.NewRegistry(clk)
	gate := governance.NewRegistrationGate(registry.NewContractRegistrar(clk), clk)
	svc, err := NewService(store, gate, WithTxManager(persistence.WrapForCell(noopTxRunner{})))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// principalCtx returns a context with a test principal (subject) and the
// canonical test tenant. Pass different subjects to distinguish actors in multi-
// principal tests; use "cell-a" as the default for single-actor tests.
func principalCtx(subject string) context.Context {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: subject, AuthMethod: "test",
	})
	return ctxkeys.WithTenantID(ctx, testTenantStr)
}

// testCtx is a convenience alias using the canonical test subject "cell-a".
func testCtx() context.Context { return principalCtx("cell-a") }

// validSubmitReq is a contract declaration that passes the gate's curated rule
// set: FMT-08 (id prefix=http), FMT-09 (kind known), CH-01 (ownerCell), CH-02
// (lifecycle), CH-03 (schemaRefs.response), REG-01 (endpoints.server present).
func validSubmitReq() *submit.Request {
	return &submit.Request{
		ID:        "http.example.foo.v1",
		Kind:      submit.RequestKindHTTP,
		OwnerCell: "registrycore",
		Lifecycle: submit.RequestLifecycleActive,
		Endpoints: &submit.RequestEndpoints{
			Server: "registrycore",
		},
		SchemaRefs: &submit.RequestSchemaRefs{
			Response: "response.schema.json",
		},
	}
}

// TestNewService_NilStore pins the required-dep fail-fast for the store field.
func TestNewService_NilStore(t *testing.T) {
	clk := clockmock.New(testEpoch)
	gate := governance.NewRegistrationGate(registry.NewContractRegistrar(clk), clk)
	if _, err := NewService(nil, gate, WithTxManager(persistence.WrapForCell(noopTxRunner{}))); err == nil {
		t.Fatal("NewService(nil store) must error (gocell:\"required\")")
	}
}

// TestNewService_NilGate pins the required-dep fail-fast for the gate field.
func TestNewService_NilGate(t *testing.T) {
	clk := clockmock.New(testEpoch)
	store := mem.NewRegistry(clk)
	if _, err := NewService(store, nil, WithTxManager(persistence.WrapForCell(noopTxRunner{}))); err == nil {
		t.Fatal("NewService(nil gate) must error (gocell:\"required\")")
	}
}

// TestNewService_NilTxRunner pins the required-dep fail-fast for the txRunner field.
func TestNewService_NilTxRunner(t *testing.T) {
	clk := clockmock.New(testEpoch)
	store := mem.NewRegistry(clk)
	gate := governance.NewRegistrationGate(registry.NewContractRegistrar(clk), clk)
	if _, err := NewService(store, gate); err == nil {
		t.Fatal("NewService(no txRunner) must error (gocell:\"required\")")
	}
}

// TestSubmit_Success: a valid declaration + authenticated principal + tenant → 201
// with the sealed RegistrationState and submitter from the principal.
func TestSubmit_Success(t *testing.T) {
	svc := newService(t)
	resp, err := svc.Submit(testCtx(), validSubmitReq())
	if err != nil {
		t.Fatalf("Submit: unexpected error %v", err)
	}
	ok, isOK := resp.(submit.Submit201JSONResponse)
	if !isOK {
		t.Fatalf("Submit returned %T, want Submit201JSONResponse", resp)
	}
	if ok.Data == nil {
		t.Fatal("Submit201JSONResponse.Data is nil")
	}
	if ok.Data.ID != "http.example.foo.v1" || ok.Data.Kind != "http" {
		t.Errorf("Data id/kind = %q/%q, want http.example.foo.v1/http", ok.Data.ID, ok.Data.Kind)
	}
	if ok.Data.State != registry.StateSubmitted().String() {
		t.Errorf("Data.State = %q, want %q (sealed)", ok.Data.State, registry.StateSubmitted().String())
	}
	if ok.Data.Submitter != "cell-a" {
		t.Errorf("Data.Submitter = %q, want cell-a (from principal)", ok.Data.Submitter)
	}
	if ok.Data.CreatedAt == "" || ok.Data.UpdatedAt == "" {
		t.Error("Data timestamps must be set")
	}
}

// TestSubmit_Duplicate: a second submit of the same id → real 409 from the
// store dedup, returned as the typed Submit409ErrorResponse.
func TestSubmit_Duplicate(t *testing.T) {
	svc := newService(t)
	ctx := testCtx()
	req := &submit.Request{
		ID:         "http.dup.v1",
		Kind:       submit.RequestKindHTTP,
		OwnerCell:  "registrycore",
		Lifecycle:  submit.RequestLifecycleActive,
		Endpoints:  &submit.RequestEndpoints{Server: "registrycore"},
		SchemaRefs: &submit.RequestSchemaRefs{Response: "response.schema.json"},
	}
	if _, err := svc.Submit(ctx, req); err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	resp, err := svc.Submit(ctx, req)
	if err != nil {
		t.Fatalf("duplicate Submit: unexpected Go error %v (want typed 409)", err)
	}
	if _, ok := resp.(submit.Submit409ErrorResponse); !ok {
		t.Fatalf("duplicate Submit returned %T, want Submit409ErrorResponse", resp)
	}
}

// TestSubmit_MissingTenant: with no tenant in context the service returns a
// typed 400 (tenant scope required).
func TestSubmit_MissingTenant(t *testing.T) {
	svc := newService(t)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "cell-a", AuthMethod: "test",
	})
	resp, err := svc.Submit(ctx, validSubmitReq())
	if err != nil {
		t.Fatalf("Submit: unexpected Go error %v (want typed 400)", err)
	}
	if _, ok := resp.(submit.Submit400ErrorResponse); !ok {
		t.Fatalf("Submit with no tenant returned %T, want Submit400ErrorResponse", resp)
	}
}

// TestSubmit_GateReject_NoEndpoints: a schema-valid payload (ownerCell +
// lifecycle present) but missing endpoints.server triggers REG-01 → gate
// denies → 400 ValidateErrorResponse.
func TestSubmit_GateReject_NoEndpoints(t *testing.T) {
	svc := newService(t)
	req := &submit.Request{
		ID:        "http.noprovider.v1",
		Kind:      submit.RequestKindHTTP,
		OwnerCell: "registrycore",
		Lifecycle: submit.RequestLifecycleActive,
		// No endpoints.server → REG-01 rejects
		SchemaRefs: &submit.RequestSchemaRefs{Response: "response.schema.json"},
	}
	resp, err := svc.Submit(testCtx(), req)
	if err != nil {
		t.Fatalf("Submit: unexpected Go error %v (want typed 400)", err)
	}
	if _, ok := resp.(submit.Submit400ErrorResponse); !ok {
		t.Fatalf("gate-rejected submit returned %T, want Submit400ErrorResponse", resp)
	}
}

// TestSubmit_GateReject_NoSchemaRefs: missing schemaRefs.response triggers
// CH-03 → gate denies → 400.
func TestSubmit_GateReject_NoSchemaRefs(t *testing.T) {
	svc := newService(t)
	req := &submit.Request{
		ID:        "http.noschemarefs.v1",
		Kind:      submit.RequestKindHTTP,
		OwnerCell: "registrycore",
		Lifecycle: submit.RequestLifecycleActive,
		Endpoints: &submit.RequestEndpoints{Server: "registrycore"},
		// No schemaRefs → CH-03 rejects for http kind
	}
	resp, err := svc.Submit(testCtx(), req)
	if err != nil {
		t.Fatalf("Submit: unexpected Go error %v (want typed 400)", err)
	}
	if _, ok := resp.(submit.Submit400ErrorResponse); !ok {
		t.Fatalf("gate-rejected submit returned %T, want Submit400ErrorResponse", resp)
	}
}

// spyPortsRegistry is a test-only ports.Registry spy that records every Create
// call and stubs the remaining methods. Used by TestSubmit_GateMustPrecedeStore_AIHard.
type spyPortsRegistry struct {
	createCalls int
}

var _ ports.Registry = (*spyPortsRegistry)(nil)

func (s *spyPortsRegistry) Create(_ context.Context, _ tenant.TenantID, _ registry.SubmitInput) (registry.ContractRegistration, error) {
	s.createCalls++
	return registry.ContractRegistration{}, nil
}

func (s *spyPortsRegistry) Transition(
	_ context.Context, _ tenant.TenantID, _ registry.AdvanceInput,
) (registry.ContractRegistration, error) {
	return registry.ContractRegistration{}, nil
}

func (s *spyPortsRegistry) Get(_ context.Context, _ tenant.TenantID, _ string) (registry.ContractRegistration, bool, error) {
	return registry.ContractRegistration{}, false, nil
}

func (s *spyPortsRegistry) List(
	_ context.Context, _ tenant.TenantID, _ query.ListParams, _ ports.ListFilter,
) ([]registry.ContractRegistration, error) {
	return nil, nil
}

func (s *spyPortsRegistry) History(_ context.Context, _ tenant.TenantID, _ string) ([]registry.RegistrationEvent, error) {
	return nil, nil
}

// TestSubmit_GateMustPrecedeStore_AIHard is the AI-HARD behavior invariant:
// when the gate rejects a contract declaration, the store's Create method is
// NEVER called. A spy registry records every Create invocation; asserting
// spy.createCalls==0 proves "持久化必经 gate" at the unit level.
func TestSubmit_GateMustPrecedeStore_AIHard(t *testing.T) {
	clk := clockmock.New(testEpoch)
	// Real gate backed by its own in-mem registrar.
	gate := governance.NewRegistrationGate(registry.NewContractRegistrar(clk), clk)

	// Spy store wired into Service — not the gate's registrar.
	spy := &spyPortsRegistry{}
	svc, err := NewService(spy, gate, WithTxManager(persistence.WrapForCell(noopTxRunner{})))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Submit a contract the gate will reject (missing endpoints.server → REG-01).
	req := &submit.Request{
		ID:        "http.gatetest.v1",
		Kind:      submit.RequestKindHTTP,
		OwnerCell: "registrycore",
		Lifecycle: submit.RequestLifecycleActive,
		// No endpoints.server → gate must deny via REG-01
		SchemaRefs: &submit.RequestSchemaRefs{Response: "response.schema.json"},
	}
	resp, err := svc.Submit(principalCtx("ai-hard-test"), req)
	if err != nil {
		t.Fatalf("Submit: unexpected Go error %v", err)
	}
	if _, ok := resp.(submit.Submit400ErrorResponse); !ok {
		t.Fatalf("expected Submit400ErrorResponse, got %T", resp)
	}
	// THE AI-HARD assertion: gate denied → store.Create was never called.
	if spy.createCalls != 0 {
		t.Fatalf("gate-rejected contract called store.Create %d times, want 0 (持久化必经 gate)", spy.createCalls)
	}
}
