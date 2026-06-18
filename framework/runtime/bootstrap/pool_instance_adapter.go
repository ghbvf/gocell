package bootstrap

// pool_instance_adapter.go — per-instance probe namespacing for fanned-out
// per-cell postgres serving pools (#2341), mirroring relay_adapter.go.
//
// In split topology cap_wiring opens N serving pools, each a
// kernel/lifecycle.ManagedResource exposing the SAME fixed probe names
// (postgres_ready, postgres_indexes_valid_ready, …). Registering N pools directly
// via WithManagedResource would collide on the global probe-name namespace —
// expandManagedResources fails fast on a duplicate name, so split topology could
// not boot at all. WithPoolInstance wraps each non-default-key pool so its probes
// are scoped by the instance id (postgres_ready → postgres_ready_<rep>); the
// colocated DefaultInstanceKey keeps the bare names (operations contract unchanged,
// zero observability regression for the single-pool shape).

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	kworker "github.com/ghbvf/gocell/framework/kernel/worker"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// poolInstanceAdapter wraps a serving-pool ManagedResource so the bootstrap
// managed-resource pipeline drives its lifecycle (Probes / Worker / Close) with
// instance-scoped probe names for a non-default infra instance. namespaced is nil
// for the colocated default key, where Probes() forwards the pool's bare-named
// probes live; for a fanned-out instance it is pre-built so N pools expose
// globally-distinct probe names.
type poolInstanceAdapter struct {
	inner      kernellifecycle.ManagedResource
	namespaced []healthz.Probe
}

// Compile-time check: the adapter implements ManagedResource.
var _ kernellifecycle.ManagedResource = (*poolInstanceAdapter)(nil)

// newPoolInstanceAdapter wraps pool for the infra instance identified by key. For
// a non-default key the pool's probe names are scoped by the instance id
// (postgres_ready → postgres_ready_<id>) so multiple fanned-out pools do not
// collide on the global probe-name namespace. The colocated default keeps the
// bare names.
func newPoolInstanceAdapter(key InfraInstanceKey, pool kernellifecycle.ManagedResource) *poolInstanceAdapter {
	a := &poolInstanceAdapter{inner: pool}
	if key == DefaultInstanceKey() {
		return a
	}
	base := pool.Probes()
	a.namespaced = make([]healthz.Probe, len(base))
	for i, p := range base {
		name, err := healthz.PoolInstanceProbeName(p.Name(), key.id)
		if err != nil {
			// Reached only on a pathological instance id: InfraInstanceKey caps id at
			// 32 chars, and the longest pool base "postgres_app_role_restricted_ready"
			// (34) + "_" + a ≥30-char id overflows probeNameMaxLen (64). Real instance
			// ids are cell IDs (≤~12 chars), so this fail-fast guards an absurd cell id
			// at startup rather than ship a malformed/duplicate probe name.
			panic(panicregister.Approved("bootstrap-pool-probe-name",
				errcode.Assertion("bootstrap: pool instance probe name composition failed (instance id too long for probe-name budget)")))
		}
		a.namespaced[i] = healthz.NewProbe(name, p.Check)
	}
	return a
}

// Probes forwards to the pool's readiness probes, instance-scoped when this
// adapter wraps a non-default infra instance.
func (a *poolInstanceAdapter) Probes() []healthz.Probe {
	if a.namespaced != nil {
		return a.namespaced
	}
	return a.inner.Probes()
}

// Worker forwards the pool's background worker (typically a nil worker for pools).
func (a *poolInstanceAdapter) Worker() kworker.Worker { return a.inner.Worker() }

// Close closes the underlying pool during LIFO teardown.
func (a *poolInstanceAdapter) Close(ctx context.Context) error { return a.inner.Close(ctx) }

// WithPoolInstance registers a fanned-out per-cell postgres serving pool as a
// ManagedResource with instance-scoped probe names (#2341). For the colocated
// DefaultInstanceKey it behaves like WithManagedResource (bare probe names); for a
// split per-cell instance it scopes the pool's probes by the key id so N pools do
// not collide on the global probe-name namespace (the duplicate-name fail-fast in
// expandManagedResources would otherwise prevent split topology from booting). A
// nil pool is a cumulative-builder noop, mirroring WithManagedResource.
func WithPoolInstance(key InfraInstanceKey, pool kernellifecycle.ManagedResource) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(pool) {
			b.managedResourceNil = true
			return
		}
		b.managedResources = append(b.managedResources, newPoolInstanceAdapter(key, pool))
	}
}
