// Package mem provides the in-memory implementation of ports.Registry
// (303-US5, #2236). It is the demo/no-PG topology store and the test double for
// the durable contract_registrations store; it wraps one kernel
// registry.ContractRegistrar per tenant, reusing the sealed state machine, input
// validation, timestamp stamping, dedup, and append-only history wholesale — no
// logic fork from the kernel.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// Compile-time check.
var _ ports.Registry = (*Registry)(nil)

const msgInvalidTenant = "registry repo: invalid tenant"

// Registry is the in-memory ports.Registry: one kernel registry.ContractRegistrar
// per tenant. The kernel registrar IS the sealed state machine + append-only
// history + dedup; this type only partitions it by tenant, so there is no logic
// fork. Cross-tenant rows are structurally unreachable (they live in a different
// inner registrar), so a mismatched access naturally returns not-found, and a
// registration id is unique only within a tenant (per-tenant dedup).
type Registry struct {
	mu         sync.Mutex
	registrars map[tenant.TenantID]*registry.ContractRegistrar
	clk        clock.Clock
}

// NewRegistry builds an empty in-memory registry. clk is the required positional
// clock (clock.Clock convention) the per-tenant registrars use to stamp events.
func NewRegistry(clk clock.Clock) *Registry {
	clock.MustHaveClock(clk, "mem.NewRegistry")
	return &Registry{
		registrars: make(map[tenant.TenantID]*registry.ContractRegistrar),
		clk:        clk,
	}
}

// registrarFor returns t's registrar, creating it on first use. It holds the map
// mutex only for the lookup/insert; the returned registrar is internally
// synchronized, so per-tenant operations run concurrently.
func (r *Registry) registrarFor(t tenant.TenantID) *registry.ContractRegistrar {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.registrars[t]
	if !ok {
		reg = registry.NewContractRegistrar(r.clk)
		r.registrars[t] = reg
	}
	return reg
}

func invalidTenant(err error) error {
	return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgInvalidTenant, err)
}

func (r *Registry) Create(_ context.Context, t tenant.TenantID, in registry.SubmitInput) (registry.ContractRegistration, error) {
	if err := t.Validate(); err != nil {
		return registry.ContractRegistration{}, invalidTenant(err)
	}
	return r.registrarFor(t).Submit(in)
}

func (r *Registry) Transition(_ context.Context, t tenant.TenantID, in registry.AdvanceInput) (registry.ContractRegistration, error) {
	if err := t.Validate(); err != nil {
		return registry.ContractRegistration{}, invalidTenant(err)
	}
	return r.registrarFor(t).Advance(in)
}

func (r *Registry) Get(_ context.Context, t tenant.TenantID, id string) (registry.ContractRegistration, bool, error) {
	if err := t.Validate(); err != nil {
		return registry.ContractRegistration{}, false, invalidTenant(err)
	}
	reg, ok := r.registrarFor(t).Get(id)
	return reg, ok, nil
}

func (r *Registry) List(_ context.Context, t tenant.TenantID, params query.ListParams) ([]registry.ContractRegistration, error) {
	if err := t.Validate(); err != nil {
		return nil, invalidTenant(err)
	}
	reg := r.registrarFor(t)
	ids := reg.AllIDs() // sorted ascending by the kernel registrar
	all := make([]registry.ContractRegistration, 0, len(ids))
	for _, id := range ids {
		if cr, ok := reg.Get(id); ok {
			all = append(all, cr)
		}
	}

	query.Sort(all, params.Sort, compareRegField)
	result, err := query.ApplyCursor(all, params, regFieldValue)
	if err != nil {
		return nil, fmt.Errorf("registry-repo: list: %w", err)
	}
	return result, nil
}

// compareRegField compares a single field of two contract registrations.
// Only "id" is supported (the fixed keyset sort column for this store).
func compareRegField(a, b registry.ContractRegistration, field string) int {
	switch field {
	case "id":
		return cmp.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

// regFieldValue extracts a cursor-comparable value from a contract registration.
// Only "id" is supported (the fixed keyset sort column for this store).
func regFieldValue(cr registry.ContractRegistration, field string) any {
	switch field {
	case "id":
		return cr.ID
	default:
		return ""
	}
}

func (r *Registry) History(_ context.Context, t tenant.TenantID, id string) ([]registry.RegistrationEvent, error) {
	if err := t.Validate(); err != nil {
		return nil, invalidTenant(err)
	}
	evs, _ := r.registrarFor(t).Events(id)
	return evs, nil
}
