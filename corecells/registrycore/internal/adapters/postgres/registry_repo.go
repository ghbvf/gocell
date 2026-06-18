package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// Compile-time check.
var _ ports.Registry = (*Registry)(nil)

// Registry is the PostgreSQL ports.Registry backed by the contract_registrations
// projection table and the append-only contract_registration_events history
// table (migration 066). (Wave 0 RED stub — real SQL lands in Wave 3.)
//
// The struct carries either a *Session (production: resolves the ambient tx via
// persistence.TxCtxKey) or a bare DBTX (test-only: injected by
// newRegistryFromDBTX), mirroring configcore.
type Registry struct {
	db      DBTX     // test-only: set by newRegistryFromDBTX
	session *Session // production path: resolves ambient tx via persistence.TxCtxKey
	clk     clock.Clock
}

// NewRegistry builds the PG registry over pool. clk stamps registration and event
// timestamps (clock.Clock convention).
func NewRegistry(pool *pgxpool.Pool, clk clock.Clock) *Registry {
	clock.MustHaveClock(clk, "registrycore/postgres.NewRegistry")
	return &Registry{session: NewSession(pool), clk: clk}
}

// resolveRead returns the DBTX for read paths (ambient tx if present, else pool).
func (r *Registry) resolveRead(ctx context.Context) DBTX {
	if r.session != nil {
		return r.session.resolve(ctx)
	}
	return r.db
}

// resolveWrite returns the DBTX for write paths, requiring an ambient tx in
// production so the projection + history writes are atomic (L1).
func (r *Registry) resolveWrite(ctx context.Context) (DBTX, error) {
	if r.session != nil {
		return r.session.resolveWrite(ctx)
	}
	return r.db, nil
}

func errStub() error {
	return errcode.New(errcode.KindInternal, errcode.ErrNotImplemented,
		"registrycore pg registry: not implemented (Wave 0 RED stub)")
}

func (r *Registry) Create(context.Context, tenant.TenantID, registry.SubmitInput) (registry.ContractRegistration, error) {
	return registry.ContractRegistration{}, errStub()
}

func (r *Registry) Transition(context.Context, tenant.TenantID, registry.AdvanceInput) (registry.ContractRegistration, error) {
	return registry.ContractRegistration{}, errStub()
}

func (r *Registry) Get(context.Context, tenant.TenantID, string) (registry.ContractRegistration, bool, error) {
	return registry.ContractRegistration{}, false, errStub()
}

func (r *Registry) List(context.Context, tenant.TenantID, string, int) ([]registry.ContractRegistration, error) {
	return nil, errStub()
}

func (r *Registry) History(context.Context, tenant.TenantID, string) ([]registry.RegistrationEvent, error) {
	return nil, errStub()
}
