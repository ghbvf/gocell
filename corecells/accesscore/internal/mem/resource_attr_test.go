package mem_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

const (
	attrTenant1 = tenant.TenantID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	attrTenant2 = tenant.TenantID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
)

func TestNewResourceAttributeProvider(t *testing.T) {
	t.Parallel()
	p := mem.NewResourceAttributeProvider()
	require.NotNil(t, p, "NewResourceAttributeProvider must return non-nil")

	// Freshly constructed provider returns empty map for any query.
	attrs, err := p.GetAttributes(context.Background(), "res-1", attrTenant1)
	require.NoError(t, err)
	assert.NotNil(t, attrs, "GetAttributes on empty provider must return non-nil map")
	assert.Empty(t, attrs, "GetAttributes on empty provider must return empty map")
}

func TestResourceAttributeProvider_GetAttributes(t *testing.T) {
	t.Parallel()

	p := mem.NewResourceAttributeProvider()
	ctx := context.Background()

	err := p.Seed(attrTenant1, "res-1", map[string][]string{
		"department": {"eng", "ops"},
		"level":      {"L5"},
	})
	require.NoError(t, err)

	cases := []struct {
		name       string
		resourceID string
		tenantID   tenant.TenantID
		wantEmpty  bool
		wantAttrs  map[string][]string
	}{
		{
			name:       "seeded_resource_returns_attrs",
			resourceID: "res-1",
			tenantID:   attrTenant1,
			wantEmpty:  false,
			wantAttrs:  map[string][]string{"department": {"eng", "ops"}, "level": {"L5"}},
		},
		{
			name:       "unknown_resource_returns_empty",
			resourceID: "res-unknown",
			tenantID:   attrTenant1,
			wantEmpty:  true,
		},
		{
			name:       "unknown_tenant_returns_empty",
			resourceID: "res-1",
			tenantID:   attrTenant2,
			wantEmpty:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attrs, err := p.GetAttributes(ctx, tc.resourceID, tc.tenantID)
			require.NoError(t, err)
			assert.NotNil(t, attrs, "GetAttributes must return non-nil map")
			if tc.wantEmpty {
				assert.Empty(t, attrs)
			} else {
				for k, wantVals := range tc.wantAttrs {
					assert.Equal(t, wantVals, attrs[k], "attr %q mismatch", k)
				}
			}
		})
	}
}

func TestResourceAttributeProvider_GetAttributes_InvalidTenant(t *testing.T) {
	t.Parallel()
	p := mem.NewResourceAttributeProvider()

	invalidTenant := tenant.TenantID("not-a-valid-uuid")
	_, err := p.GetAttributes(context.Background(), "res-1", invalidTenant)
	require.Error(t, err, "invalid tenant must return error")
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInvalid, ce.Kind)
}

func TestResourceAttributeProvider_Seed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := mem.NewResourceAttributeProvider()

	// Seed sets attributes.
	err := p.Seed(attrTenant1, "res-2", map[string][]string{"role": {"admin"}})
	require.NoError(t, err)

	attrs, err := p.GetAttributes(ctx, "res-2", attrTenant1)
	require.NoError(t, err)
	assert.Equal(t, []string{"admin"}, attrs["role"])

	// Re-seeding replaces all attributes.
	err = p.Seed(attrTenant1, "res-2", map[string][]string{"role": {"viewer"}})
	require.NoError(t, err)

	attrs, err = p.GetAttributes(ctx, "res-2", attrTenant1)
	require.NoError(t, err)
	assert.Equal(t, []string{"viewer"}, attrs["role"])
	assert.NotContains(t, attrs, "level", "re-seed must replace, not merge")

	// Seed with nil removes the resource.
	err = p.Seed(attrTenant1, "res-2", nil)
	require.NoError(t, err)

	attrs, err = p.GetAttributes(ctx, "res-2", attrTenant1)
	require.NoError(t, err)
	assert.Empty(t, attrs, "after nil seed resource must be absent")
}

func TestResourceAttributeProvider_Seed_InvalidTenant(t *testing.T) {
	t.Parallel()
	p := mem.NewResourceAttributeProvider()

	err := p.Seed(tenant.TenantID("bad-uuid"), "res-1", map[string][]string{"k": {"v"}})
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInvalid, ce.Kind)
}

// TestResourceAttributeProvider_CloneIndependence verifies that mutating the
// map returned by GetAttributes does not affect the stored state, and that
// mutations to the map passed to Seed are not reflected in subsequent GetAttributes
// calls (mirrors the clone-independence test for PolicyRepository).
func TestResourceAttributeProvider_CloneIndependence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := mem.NewResourceAttributeProvider()

	original := map[string][]string{"department": {"eng", "ops"}}
	err := p.Seed(attrTenant1, "res-clone", original)
	require.NoError(t, err)

	// Mutate the input map — must not affect stored state.
	original["department"] = []string{"mutated-after-seed"}
	original["newkey"] = []string{"x"}

	got, err := p.GetAttributes(ctx, "res-clone", attrTenant1)
	require.NoError(t, err)
	assert.Equal(t, []string{"eng", "ops"}, got["department"],
		"seed input mutation must not affect stored state")
	assert.NotContains(t, got, "newkey",
		"key added after Seed must not appear in stored state")

	// Mutate the returned map — must not affect stored state.
	got["department"][0] = "mutated-returned-value"
	got["newkey2"] = []string{"y"}

	got2, err := p.GetAttributes(ctx, "res-clone", attrTenant1)
	require.NoError(t, err)
	assert.Equal(t, []string{"eng", "ops"}, got2["department"],
		"returned map mutation must not affect stored state")
	assert.NotContains(t, got2, "newkey2",
		"key added to returned map must not appear in stored state")
}

func TestResourceAttributeProvider_CrossTenantIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := mem.NewResourceAttributeProvider()

	err := p.Seed(attrTenant1, "res-iso", map[string][]string{"k": {"v1"}})
	require.NoError(t, err)

	// attrTenant2 must not see attrTenant1's attributes.
	attrs, err := p.GetAttributes(ctx, "res-iso", attrTenant2)
	require.NoError(t, err)
	assert.Empty(t, attrs, "cross-tenant isolation: tenant2 must not see tenant1 resource")
}

func TestResourceAttributeProvider_Concurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := mem.NewResourceAttributeProvider()

	var wg sync.WaitGroup
	const goroutines = 20
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resourceID := "res-concurrent"
			if idx%2 == 0 {
				_ = p.Seed(attrTenant1, resourceID, map[string][]string{"idx": {string(rune('0' + idx%10))}})
			} else {
				_, _ = p.GetAttributes(ctx, resourceID, attrTenant1)
			}
		}(i)
	}
	wg.Wait()
}
