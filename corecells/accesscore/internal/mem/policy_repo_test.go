package mem_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
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

func mustCreate(t *testing.T, repo *mem.PolicyRepository, tid tenant.TenantID, p *abac.Policy) {
	t.Helper()
	if err := repo.Create(context.Background(), tid, p); err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}
}

func TestPolicyRepository_CreateAndGetByID_RoundTrip(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	mustCreate(t, repo, testTenant1, p)

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
	if got.Version != 1 {
		t.Errorf("GetByID().Version = %d, want 1 after Create", got.Version)
	}
}

func TestPolicyRepository_CrossTenantIsolation(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	mustCreate(t, repo, testTenant1, p)

	// T2 should not see T1's policy
	_, err := repo.GetByID(ctx, testTenant2, "policy-1")
	if err == nil {
		t.Fatal("GetByID() expected not-found error for cross-tenant access, got nil")
	}
	assertKind(t, err, errcode.KindNotFound, "GetByID() cross-tenant")

	// Delete in T2 should also fail
	if _, err := repo.Delete(ctx, testTenant2, "policy-1", 1); err == nil {
		t.Fatal("Delete() expected not-found error for cross-tenant access, got nil")
	}
}

func TestPolicyRepository_ListByTenant(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	// Create 2 policies in T1, 1 in T2
	p1 := makeTestPolicy("policy-a", testTenant1)
	p2 := makeTestPolicy("policy-b", testTenant1)
	p3 := makeTestPolicy("policy-c", testTenant2)

	for _, p := range []*abac.Policy{p1, p2, p3} {
		mustCreate(t, repo, p.TenantID, p)
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

	mustCreate(t, repo, testTenant1, p)
	if _, err := repo.Delete(ctx, testTenant1, "policy-1", 1); err != nil {
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

	_, err := repo.Delete(ctx, testTenant1, "nonexistent", 1)
	if err == nil {
		t.Fatal("Delete() expected not-found error, got nil")
	}
	assertKind(t, err, errcode.KindNotFound, "Delete() nonexistent")
}

func TestPolicyRepository_DeleteVersionConflict(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)
	mustCreate(t, repo, testTenant1, p)

	_, err := repo.Delete(ctx, testTenant1, "policy-1", 99 /* wrong version */)
	if err == nil {
		t.Fatal("Delete() with wrong version expected KindConflict, got nil")
	}
	assertKind(t, err, errcode.KindConflict, "Delete() version conflict")
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

func TestPolicyRepository_CreateInvalidPolicy(t *testing.T) {
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
	err := repo.Create(ctx, testTenant1, invalid)
	if err == nil {
		t.Fatal("Create() expected validation error, got nil")
	}
}

func TestPolicyRepository_CreateConflict(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)
	mustCreate(t, repo, testTenant1, p)

	// Second Create with same id must fail.
	err := repo.Create(ctx, testTenant1, makeTestPolicy("policy-1", testTenant1))
	if err == nil {
		t.Fatal("Create() second call with same id expected KindConflict, got nil")
	}
	assertKind(t, err, errcode.KindConflict, "Create() duplicate")
}

func TestPolicyRepository_CreateTenantMismatch(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	// Policy claims T1 but we pass T2
	p := makeTestPolicy("policy-1", testTenant1)
	err := repo.Create(ctx, testTenant2, p)
	if err == nil {
		t.Fatal("Create() expected error when p.TenantID != t, got nil")
	}
	assertKind(t, err, errcode.KindInvalid, "Create() tenant mismatch")
}

func TestPolicyRepository_UpdateCASSuccess(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)
	mustCreate(t, repo, testTenant1, p)

	updated := makeTestPolicy("policy-1", testTenant1)
	updated.Name = "Updated Name"
	got, err := repo.Update(ctx, testTenant1, "policy-1", 1, updated)
	if err != nil {
		t.Fatalf("Update() unexpected error: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("Update() returned Version = %d, want 2", got.Version)
	}
	if got.Name != "Updated Name" {
		t.Errorf("Update() returned Name = %q, want %q", got.Name, "Updated Name")
	}

	stored, err := repo.GetByID(ctx, testTenant1, "policy-1")
	if err != nil {
		t.Fatalf("GetByID() after Update unexpected error: %v", err)
	}
	if stored.Version != 2 {
		t.Errorf("GetByID() after Update Version = %d, want 2", stored.Version)
	}
	if stored.Name != "Updated Name" {
		t.Errorf("GetByID() after Update Name = %q, want %q", stored.Name, "Updated Name")
	}
}

func TestPolicyRepository_UpdateVersionConflict(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)
	mustCreate(t, repo, testTenant1, p)

	updated := makeTestPolicy("policy-1", testTenant1)
	_, err := repo.Update(ctx, testTenant1, "policy-1", 99 /* wrong */, updated)
	if err == nil {
		t.Fatal("Update() with wrong version expected KindConflict, got nil")
	}
	assertKind(t, err, errcode.KindConflict, "Update() version conflict")
}

func TestPolicyRepository_UpdateNotFound(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	updated := makeTestPolicy("nonexistent", testTenant1)
	_, err := repo.Update(ctx, testTenant1, "nonexistent", 1, updated)
	if err == nil {
		t.Fatal("Update() on nonexistent expected KindNotFound, got nil")
	}
	assertKind(t, err, errcode.KindNotFound, "Update() nonexistent")
}

func makeTestPolicyWithFieldMask(policyID string, tid tenant.TenantID) *abac.Policy {
	p := makeTestPolicy(policyID, tid)
	p.Rules[0].Obligations.FieldMask.Fields = []string{"email", "phone"}
	return p
}

func TestPolicyRepository_ReturnedCloneIsIndependent(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicyWithFieldMask("policy-1", testTenant1)

	mustCreate(t, repo, testTenant1, p)

	got, err := repo.GetByID(ctx, testTenant1, "policy-1")
	if err != nil {
		t.Fatalf("GetByID() unexpected error: %v", err)
	}

	// Mutate the returned policy — top-level fields, rule fields, and FieldMask
	got.Name = "mutated name"
	got.Rules[0].Name = "mutated rule name"
	got.Rules[0].Obligations.FieldMask.Fields[0] = "mutated_field"

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
	if len(got2.Rules[0].Obligations.FieldMask.Fields) == 0 {
		t.Fatal("Clone has no FieldMask.Fields — store lost the data")
	}
	if got2.Rules[0].Obligations.FieldMask.Fields[0] == "mutated_field" {
		t.Errorf("Clone mutation leaked into store: Rules[0].Obligations.FieldMask.Fields[0] = %q",
			got2.Rules[0].Obligations.FieldMask.Fields[0])
	}
}

func TestPolicyRepository_ListReturnsClones(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()
	p := makeTestPolicy("policy-1", testTenant1)

	mustCreate(t, repo, testTenant1, p)

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

func TestPolicyRepository_ConcurrentCreateGet(t *testing.T) {
	t.Parallel()

	repo := mem.NewPolicyRepository()
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := &concurrentPolicyErrors{}
	const goroutines = 20

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			runConcurrentPolicyOperation(ctx, repo, idx, errs)
		}(i)
	}
	wg.Wait()

	// No unexpected infrastructure errors from any operation
	errs.assertEmpty(t)
}

type concurrentPolicyErrors struct {
	mu         sync.Mutex
	createErrs []error
	getErrs    []error
	listErrs   []error
	deleteErrs []error
}

func (e *concurrentPolicyErrors) addCreate(err error) { e.add(&e.createErrs, err) }
func (e *concurrentPolicyErrors) addGet(err error)    { e.add(&e.getErrs, err) }
func (e *concurrentPolicyErrors) addList(err error)   { e.add(&e.listErrs, err) }
func (e *concurrentPolicyErrors) addDelete(err error) { e.add(&e.deleteErrs, err) }

func (e *concurrentPolicyErrors) add(dst *[]error, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	*dst = append(*dst, err)
}

func (e *concurrentPolicyErrors) assertEmpty(t *testing.T) {
	t.Helper()

	if len(e.createErrs) > 0 {
		t.Errorf("concurrent Create() produced %d unexpected error(s): first=%v", len(e.createErrs), e.createErrs[0])
	}
	if len(e.getErrs) > 0 {
		t.Errorf("concurrent GetByID() produced %d unexpected error(s): first=%v", len(e.getErrs), e.getErrs[0])
	}
	if len(e.listErrs) > 0 {
		t.Errorf("concurrent ListByTenant() produced %d unexpected error(s): first=%v", len(e.listErrs), e.listErrs[0])
	}
	if len(e.deleteErrs) > 0 {
		t.Errorf("concurrent Delete() produced %d unexpected error(s): first=%v", len(e.deleteErrs), e.deleteErrs[0])
	}
}

func runConcurrentPolicyOperation(ctx context.Context, repo *mem.PolicyRepository, idx int, errs *concurrentPolicyErrors) {
	policyID := "policy-concurrent"
	p := makeTestPolicy(policyID, testTenant1)

	// Create may return KindConflict if another goroutine already created it.
	if err := repo.Create(ctx, testTenant1, p); err != nil && !isConflictError(err) {
		errs.addCreate(err)
	}
	if _, err := repo.GetByID(ctx, testTenant1, policyID); err != nil && !isNotFoundError(err) {
		errs.addGet(err)
	}
	if _, err := repo.ListByTenant(ctx, testTenant1); err != nil {
		errs.addList(err)
	}
	// Interleave Delete on even goroutines to exercise concurrent write paths.
	if idx%2 == 0 {
		if _, err := repo.Delete(ctx, testTenant1, policyID, 1); err != nil {
			if !isNotFoundError(err) && !isConflictError(err) {
				errs.addDelete(err)
			}
		}
	}
}

func isNotFoundError(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Kind == errcode.KindNotFound
}

func isConflictError(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Kind == errcode.KindConflict
}
