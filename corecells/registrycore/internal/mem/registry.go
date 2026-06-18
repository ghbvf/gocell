// Package mem provides the in-memory implementation of ports.Registry
// (303-US5, #2236). It is the demo/no-PG topology store and the test double for
// the durable contract_registrations store; it wraps one kernel
// registry.ContractRegistrar per tenant, reusing the sealed state machine,
// input validation, timestamp stamping, and append-only history wholesale (no
// logic fork from the kernel).
package mem

import (
	"context"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// Compile-time check.
var _ ports.Registry = (*Registry)(nil)

// Registry is the in-memory ports.Registry. (Wave 0 RED stub — real
// per-tenant ContractRegistrar wrapping lands in Wave 2.)
type Registry struct {
	clk clock.Clock
}

// NewRegistry builds an empty in-memory registry. clk is the required positional
// clock (clock.Clock convention) the per-tenant registrars use to stamp events.
func NewRegistry(clk clock.Clock) *Registry {
	clock.MustHaveClock(clk, "mem.NewRegistry")
	return &Registry{clk: clk}
}

func errStub() error {
	return errcode.New(errcode.KindInternal, errcode.ErrNotImplemented,
		"registrycore mem registry: not implemented (Wave 0 RED stub)")
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
