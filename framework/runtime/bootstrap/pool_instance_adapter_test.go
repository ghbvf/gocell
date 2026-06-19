package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
	kworker "github.com/ghbvf/gocell/framework/kernel/worker"
)

// fakePoolResource is a minimal ManagedResource exposing the fixed postgres
// serving-pool probe names, so the per-instance namespacing (#2341) can be tested
// without a live pool.
type fakePoolResource struct {
	closed bool
}

func (f *fakePoolResource) Probes() []healthz.Probe {
	noop := func(context.Context) error { return nil }
	return []healthz.Probe{
		healthz.NewProbe(healthz.MustProbeName("postgres_ready"), noop),
		healthz.NewProbe(healthz.MustProbeName("postgres_indexes_valid_ready"), noop),
		healthz.NewProbe(healthz.MustProbeName("postgres_app_role_restricted_ready"), noop),
	}
}
func (f *fakePoolResource) Worker() kworker.Worker      { return nil }
func (f *fakePoolResource) Close(context.Context) error { f.closed = true; return nil }

func probeNames(ps []healthz.Probe) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p.Name())
	}
	return out
}

// TestPoolInstanceAdapter_DefaultKey_BareNames: the colocated DefaultInstanceKey
// keeps the pool's bare probe names — zero observability regression for the
// single-pool shape.
func TestPoolInstanceAdapter_DefaultKey_BareNames(t *testing.T) {
	t.Parallel()
	a := newPoolInstanceAdapter(DefaultInstanceKey(), &fakePoolResource{})
	assert.Equal(t,
		[]string{"postgres_ready", "postgres_indexes_valid_ready", "postgres_app_role_restricted_ready"},
		probeNames(a.Probes()),
		"default key must forward bare probe names")
}

// TestPoolInstanceAdapter_SplitKey_Namespaced: a non-default key scopes every pool
// probe by the instance id, so N fanned-out pools expose globally-distinct names
// (the fix for the split-topology duplicate-probe boot failure).
func TestPoolInstanceAdapter_SplitKey_Namespaced(t *testing.T) {
	t.Parallel()
	a := newPoolInstanceAdapter(NewInfraInstanceKey("auditcore"), &fakePoolResource{})
	assert.Equal(t,
		[]string{"postgres_ready_auditcore", "postgres_indexes_valid_ready_auditcore", "postgres_app_role_restricted_ready_auditcore"},
		probeNames(a.Probes()),
		"non-default key must namespace every pool probe by the instance id")
}

// TestWithPoolInstance_TwoSplitPools_NoProbeCollision is the core #2341 regression
// guard: two per-cell pools registered via WithPoolInstance with distinct keys must
// expand without the duplicate-probe fail-fast that blocked split topology before
// this fix. (Two bare-named pools would collide; the namespacing prevents it.)
func TestWithPoolInstance_TwoSplitPools_NoProbeCollision(t *testing.T) {
	t.Parallel()
	b := &Bootstrap{}
	WithPoolInstance(NewInfraInstanceKey("accesscore"), &fakePoolResource{})(b)
	WithPoolInstance(NewInfraInstanceKey("auditcore"), &fakePoolResource{})(b)
	require.Len(t, b.managedResources, 2, "both per-cell pools must register")

	// expandManagedResources fails fast on a duplicate probe name; with per-instance
	// namespacing the two pools' probe names are globally distinct, so it succeeds.
	require.NoError(t, b.expandManagedResources(),
		"split per-cell pools must not collide on probe names (#2341)")
}

// TestWithPoolInstance_NilPool_Noop: a nil pool is a cumulative-builder noop that
// flags the nil-resource configuration error, mirroring WithManagedResource.
func TestWithPoolInstance_NilPool_Noop(t *testing.T) {
	t.Parallel()
	b := &Bootstrap{}
	WithPoolInstance(DefaultInstanceKey(), nil)(b)
	assert.Empty(t, b.managedResources, "nil pool must not register a resource")
	assert.True(t, b.managedResourceNil, "nil pool must flag the configuration error")
}

// TestWithPoolInstance_ClosePropagates: closing the adapter (LIFO teardown) closes
// the underlying pool.
func TestWithPoolInstance_ClosePropagates(t *testing.T) {
	t.Parallel()
	fp := &fakePoolResource{}
	a := newPoolInstanceAdapter(NewInfraInstanceKey("configcore"), fp)
	require.NoError(t, a.Close(context.Background()))
	assert.True(t, fp.closed, "closing the adapter must close the underlying pool")
}
