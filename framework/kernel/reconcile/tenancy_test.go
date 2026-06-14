package reconcile

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTenancy_ZeroValueIsUnset proves the sealed Tenancy zero value is the
// "unset" sentinel (Build rejects it, #1954) and the two minters are NOT unset.
// This is the load-bearing fail-fast predicate: a consumer who writes
// reconcile.Tenancy{} (the only externally-constructible value — the mode field
// is unexported) must be rejected at Build.
func TestTenancy_ZeroValueIsUnset(t *testing.T) {
	t.Parallel()
	var z Tenancy
	assert.True(t, z.IsUnset(), "zero-value Tenancy{} must report IsUnset (Build rejects it)")
	assert.False(t, SingleTenant().IsUnset(), "SingleTenant() must not be unset")
	assert.False(t, TenantScoped().IsUnset(), "TenantScoped() must not be unset")
}

// TestTenancy_MinterStableIdentity proves the two minters return stable,
// comparable, distinct values (Tenancy is a comparable struct over a single
// uint8). Mirrors authz.TestPermAuditRead_StableIdentity — an accessor func
// returns the same package-private singleton every call.
func TestTenancy_MinterStableIdentity(t *testing.T) {
	t.Parallel()
	assert.Equal(t, SingleTenant(), SingleTenant(), "SingleTenant() must be stable across calls")
	assert.Equal(t, TenantScoped(), TenantScoped(), "TenantScoped() must be stable across calls")
	assert.NotEqual(t, SingleTenant(), TenantScoped(), "the two stances must be distinct values")
}
