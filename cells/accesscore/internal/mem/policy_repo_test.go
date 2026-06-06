package mem_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// assertKind is a helper that asserts err is a *errcode.Error with the given Kind.
func assertKind(t *testing.T, err error, want errcode.Kind, label string) {
	t.Helper()
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Errorf("%s: expected *errcode.Error, got %T: %v", label, err, err)
		return
	}
	if ec.Kind != want {
		t.Errorf("%s: expected Kind %v, got %v (err: %v)", label, want, ec.Kind, err)
	}
}

const (
	testTenant1 = tenant.TenantID("11111111-1111-1111-1111-111111111111")
	testTenant2 = tenant.TenantID("22222222-2222-2222-2222-222222222222")
)

func makeTestPolicy(policyID string, tid tenant.TenantID) *abac.Policy {
	return &abac.Policy{
		ID:       policyID,
		TenantID: tid,
		Name:     "Policy " + policyID,
		Rules: []abac.Rule{
			{
				ID:     "rule-1",
				Name:   "Allow Eng",
				Effect: authz.EffectAllow,
				Conditions: []abac.Condition{
					{
						Source:   abac.SourceSubject,
						Key:      "department",
						Operator: abac.OpEquals,
						Values:   []string{"eng"},
					},
				},
			},
		},
	}
}

func TestPolicyRepository_SaveAndGetByID_RoundTrip(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	if err := repo.Save(ctx, testTenant1, p); err != nil {
		t.Fatalf("Save() unexpected error: %v", err)
	}

	got, err := repo.GetByID(ctx, testTenant1, "policy-1")
	if err != nil {
		t.Fatalf("GetByID() unexpected error: %v", err)
	}
	if got.ID != p.ID {
		t.Errorf("GetByID().ID = %q, want %q", got.ID, p.ID)
	}
	if got.Name != p.Name {
		t.Errorf("GetByID().Name = %q, want %q", got.Name, p.Name)
	}
}

func TestPolicyRepository_CrossTenantIsolation(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	if err := repo.Save(ctx, testTenant1, p); err != nil {
		t.Fatalf("Save() unexpected error: %v", err)
	}

	// T2 should not see T1's policy
	_, err := repo.GetByID(ctx, testTenant2, "policy-1")
	if err == nil {
		t.Fatal("GetByID() expected not-found error for cross-tenant access, got nil")
	}
	assertKind(t, err, errcode.KindNotFound, "GetByID() cross-tenant")

	// Delete in T2 should also fail
	if err := repo.Delete(ctx, testTenant2, "policy-1"); err == nil {
		t.Fatal("Delete() expected not-found error for cross-tenant access, got nil")
	}
}

func TestPolicyRepository_ListByTenant(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	// Save 2 policies in T1, 1 in T2
	p1 := makeTestPolicy("policy-a", testTenant1)
	p2 := makeTestPolicy("policy-b", testTenant1)
	p3 := makeTestPolicy("policy-c", testTenant2)

	for _, p := range []*abac.Policy{p1, p2, p3} {
		if err := repo.Save(ctx, p.TenantID, p); err != nil {
			t.Fatalf("Save(%s) unexpected error: %v", p.ID, err)
		}
	}

	t1List, err := repo.ListByTenant(ctx, testTenant1)
	if err != nil {
		t.Fatalf("ListByTenant(T1) unexpected error: %v", err)
	}
	if len(t1List) != 2 {
		t.Errorf("ListByTenant(T1) got %d policies, want 2", len(t1List))
	}

	t2List, err := repo.ListByTenant(ctx, testTenant2)
	if err != nil {
		t.Fatalf("ListByTenant(T2) unexpected error: %v", err)
	}
	if len(t2List) != 1 {
		t.Errorf("ListByTenant(T2) got %d policies, want 1", len(t2List))
	}
}

func TestPolicyRepository_DeleteThenGetByID(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	if err := repo.Save(ctx, testTenant1, p); err != nil {
		t.Fatalf("Save() unexpected error: %v", err)
	}
	if err := repo.Delete(ctx, testTenant1, "policy-1"); err != nil {
		t.Fatalf("Delete() unexpected error: %v", err)
	}

	_, err := repo.GetByID(ctx, testTenant1, "policy-1")
	if err == nil {
		t.Fatal("GetByID() expected not-found after delete, got nil")
	}
	assertKind(t, err, errcode.KindNotFound, "GetByID() after delete")
}

func TestPolicyRepository_DeleteMissingID(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	err := repo.Delete(ctx, testTenant1, "nonexistent")
	if err == nil {
		t.Fatal("Delete() expected not-found error, got nil")
	}
	assertKind(t, err, errcode.KindNotFound, "Delete() nonexistent")
}

func TestPolicyRepository_GetByIDMissing(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	_, err := repo.GetByID(ctx, testTenant1, "nonexistent")
	if err == nil {
		t.Fatal("GetByID() expected not-found error, got nil")
	}
	assertKind(t, err, errcode.KindNotFound, "GetByID() nonexistent")
}

func TestPolicyRepository_SaveInvalidPolicy(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	// Invalid: empty Name
	invalid := &abac.Policy{
		ID:       "policy-1",
		TenantID: testTenant1,
		Name:     "", // invalid
		Rules:    []abac.Rule{makeTestPolicy("x", testTenant1).Rules[0]},
	}
	err := repo.Save(ctx, testTenant1, invalid)
	if err == nil {
		t.Fatal("Save() expected validation error, got nil")
	}
}

func TestPolicyRepository_SaveTenantMismatch(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	// Policy claims T1 but we pass T2
	p := makeTestPolicy("policy-1", testTenant1)
	err := repo.Save(ctx, testTenant2, p)
	if err == nil {
		t.Fatal("Save() expected error when p.TenantID != t, got nil")
	}
	assertKind(t, err, errcode.KindInvalid, "Save() tenant mismatch")
}

func TestPolicyRepository_ReturnedCloneIsIndependent(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	if err := repo.Save(ctx, testTenant1, p); err != nil {
		t.Fatalf("Save() unexpected error: %v", err)
	}

	got, err := repo.GetByID(ctx, testTenant1, "policy-1")
	if err != nil {
		t.Fatalf("GetByID() unexpected error: %v", err)
	}

	// Mutate the returned policy
	got.Name = "mutated name"
	got.Rules[0].Name = "mutated rule name"

	// Re-get should still have original values
	got2, err := repo.GetByID(ctx, testTenant1, "policy-1")
	if err != nil {
		t.Fatalf("GetByID() second call unexpected error: %v", err)
	}
	if got2.Name == "mutated name" {
		t.Errorf("Clone mutation leaked into store: Name = %q", got2.Name)
	}
	if got2.Rules[0].Name == "mutated rule name" {
		t.Errorf("Clone mutation leaked into store: Rules[0].Name = %q", got2.Rules[0].Name)
	}
}

func TestPolicyRepository_ListReturnsClones(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	if err := repo.Save(ctx, testTenant1, p); err != nil {
		t.Fatalf("Save() unexpected error: %v", err)
	}

	list, err := repo.ListByTenant(ctx, testTenant1)
	if err != nil {
		t.Fatalf("ListByTenant() unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListByTenant() got %d, want 1", len(list))
	}

	// Mutate returned list item
	list[0].Name = "mutated"

	// Re-list should be unaffected
	list2, err := repo.ListByTenant(ctx, testTenant1)
	if err != nil {
		t.Fatalf("ListByTenant() second unexpected error: %v", err)
	}
	if list2[0].Name == "mutated" {
		t.Errorf("List clone mutation leaked into store: Name = %q", list2[0].Name)
	}
}

func TestPolicyRepository_Upsert(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	if err := repo.Save(ctx, testTenant1, p); err != nil {
		t.Fatalf("first Save() unexpected error: %v", err)
	}

	// Update
	p.Name = "Updated Name"
	if err := repo.Save(ctx, testTenant1, p); err != nil {
		t.Fatalf("second Save() unexpected error: %v", err)
	}

	got, err := repo.GetByID(ctx, testTenant1, "policy-1")
	if err != nil {
		t.Fatalf("GetByID() unexpected error: %v", err)
	}
	if got.Name != "Updated Name" {
		t.Errorf("GetByID().Name = %q, want %q", got.Name, "Updated Name")
	}
}

func TestPolicyRepository_ConcurrentSaveGet(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	var wg sync.WaitGroup
	const goroutines = 20

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policyID := "policy-concurrent"
			p := makeTestPolicy(policyID, testTenant1)
			_ = repo.Save(ctx, testTenant1, p)
			_, _ = repo.GetByID(ctx, testTenant1, policyID)
			_, _ = repo.ListByTenant(ctx, testTenant1)
		}()
	}
	wg.Wait()
}
