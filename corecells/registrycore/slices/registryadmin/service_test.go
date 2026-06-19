package registryadmin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/mem"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// TestNewService_NilStore: store is a gocell:"required" dep, so validateRequired
// (generated in service_required_gen.go) must fail-fast on a nil store.
func TestNewService_NilStore(t *testing.T) {
	if _, err := NewService(nil, WithTxManager(persistence.WrapForCell(noopTxRunner{}))); err == nil {
		t.Fatal("NewService(nil store) must fail-fast — store is gocell:\"required\"")
	}
}

// TestNewService_NilTxRunner: txRunner is a gocell:"required" dep; omitting
// WithTxManager leaves it nil, so NewService must fail-fast.
func TestNewService_NilTxRunner(t *testing.T) {
	store := mem.NewRegistry(clockmock.New(testEpoch))
	if _, err := NewService(store); err == nil {
		t.Fatal("NewService without WithTxManager must fail-fast — txRunner is gocell:\"required\"")
	}
}

// TestNewService_OK_WithLogger: with both required deps wired and an explicit
// logger, NewService succeeds (covers the WithLogger option path).
func TestNewService_OK_WithLogger(t *testing.T) {
	store := mem.NewRegistry(clockmock.New(testEpoch))
	svc, err := NewService(store,
		WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		WithLogger(slog.New(slog.NewTextHandler(newDiscard(), nil))),
	)
	if err != nil {
		t.Fatalf("NewService with all required deps + logger must succeed, got: %v", err)
	}
	if svc == nil {
		t.Fatal("NewService returned nil service")
	}
}

// newDiscard returns an io.Writer that drops all writes (test logger sink).
func newDiscard() *discardWriter { return &discardWriter{} }

type discardWriter struct{}

func (*discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// faultStore is a ports.Registry stub whose Transition returns a fixed error, to
// exercise the errInternal (undeclared 5xx) classification path — the branch where
// the store fails with something other than a recognized 4xx errcode. The other
// methods are unused by the admin transition path.
type faultStore struct{ err error }

func (s faultStore) Create(context.Context, tenant.TenantID, registry.SubmitInput) (registry.ContractRegistration, error) {
	return registry.ContractRegistration{}, s.err
}

func (s faultStore) Transition(context.Context, tenant.TenantID, registry.AdvanceInput) (registry.ContractRegistration, error) {
	return registry.ContractRegistration{}, s.err
}

func (faultStore) Get(context.Context, tenant.TenantID, string) (registry.ContractRegistration, bool, error) {
	return registry.ContractRegistration{}, false, nil
}

func (faultStore) List(context.Context, tenant.TenantID, query.ListParams, ports.ListFilter) ([]registry.ContractRegistration, error) {
	return nil, nil
}

func (faultStore) History(context.Context, tenant.TenantID, string) ([]registry.RegistrationEvent, error) {
	return nil, nil
}

var _ ports.Registry = faultStore{}

// TestContractApproveServe_InternalError: a non-errcode store error classifies as
// errInternal → undeclared framework 5xx (handler WriteError fallback), and
// logTransitionFailure records the scrubbed error server-side.
func TestContractApproveServe_InternalError(t *testing.T) {
	store := faultStore{err: errors.New("boom: store unreachable")}
	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), approvePath(seedID), `{}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestContractRejectServe_InternalError: same errInternal 5xx path via reject.
func TestContractRejectServe_InternalError(t *testing.T) {
	store := faultStore{err: errors.New("boom: store unreachable")}
	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), rejectPath(seedID), `{}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestContractRetireServe_InternalError: same errInternal 5xx path via retire.
func TestContractRetireServe_InternalError(t *testing.T) {
	store := faultStore{err: errors.New("boom: store unreachable")}
	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), retirePath(seedID), `{}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestContractApproveServe_UnrecognizedErrcode: an errcode whose Code is not one of
// the classified 4xx buckets (e.g. ErrRegistrationRepoQuery, an infra error) hits
// the classifyTransitionErr default branch → errInternal → 5xx.
func TestContractApproveServe_UnrecognizedErrcode(t *testing.T) {
	store := faultStore{err: errcode.New(errcode.KindInternal, errcode.ErrRegistrationRepoQuery,
		"registry repo query failed")}
	rec := postAdmin(t, newAdminMux(t, store), adminCtx(allowAuthorizer()), approvePath(seedID), `{}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}
