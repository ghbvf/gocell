package conformance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/tenant"
)

const (
	msgPolicyCreateUnexpected       = "Create() unexpected error: %v"
	msgPolicyGetByIDUnexpected      = "GetByID() unexpected error: %v"
	msgPolicyListByTenantUnexpected = "ListByTenant() unexpected error: %v"
)

// PolicyRepoFactory constructs a fresh ports.PolicyRepository for use in a
// single conformance sub-test. The factory must return a zero-state (empty)
// repository; state accumulated across calls is a test error. No TxRunner is
// required: PolicyRepository implementations at mem-tier manage their own
// concurrency primitives, and PG-tier PolicyRepository (when added) will
// supply its own factory with any required DB plumbing.
type PolicyRepoFactory func(t *testing.T) ports.PolicyRepository

// RunPolicyRepoConformance executes the full PolicyRepository conformance suite
// against any implementation provided by factory. Each sub-test receives a
// freshly constructed repository from factory to avoid inter-test state leakage.
//
// Implementations MUST enroll by calling RunPolicyRepoConformance from a
// _test.go file in their own package. This requirement is enforced by the
// POLICYREPO-CONFORMANCE-ENROLLMENT-01 archtest
// (tools/archtest/policy_repo_conformance_enrollment_test.go). Example:
//
//	func TestMemPolicyRepo_Conformance(t *testing.T) {
//	    conformance.RunPolicyRepoConformance(t, func(t *testing.T) ports.PolicyRepository {
//	        return mem.NewPolicyRepository()
//	    })
//	}
func RunPolicyRepoConformance(t *testing.T, factory PolicyRepoFactory) {
	t.Helper()
	t.Run("CreateAndGetByID_RoundTrip", func(t *testing.T) {
		conformPolicyCreateAndGetByID(t, factory)
	})
	t.Run("GetByID_NotFound", func(t *testing.T) {
		err := conformPolicyGetByIDNotFound(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthPolicyNotFound)
	})
	t.Run("ListByTenant_Empty", func(t *testing.T) {
		conformPolicyListByTenantEmpty(t, factory)
	})
	t.Run("ListByTenant_ReturnsSavedPolicies", func(t *testing.T) {
		conformPolicyListByTenant(t, factory)
	})
	t.Run("Create_Conflict", func(t *testing.T) {
		err := conformPolicyCreateConflict(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthPolicyDuplicate)
	})
	t.Run("Update_CAS_Success", func(t *testing.T) {
		conformPolicyUpdateCASSuccess(t, factory)
	})
	t.Run("Update_VersionConflict", func(t *testing.T) {
		err := conformPolicyUpdateVersionConflict(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrVersionConflict)
	})
	t.Run("Update_NotFound", func(t *testing.T) {
		err := conformPolicyUpdateNotFound(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthPolicyNotFound)
	})
	t.Run("Delete_CAS_Success", func(t *testing.T) {
		conformPolicyDeleteCASSuccess(t, factory)
	})
	t.Run("Delete_VersionConflict", func(t *testing.T) {
		err := conformPolicyDeleteVersionConflict(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrVersionConflict)
	})
	t.Run("Delete_NotFound", func(t *testing.T) {
		err := conformPolicyDeleteNotFound(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthPolicyNotFound)
	})
	t.Run("CrossTenant_Isolation", func(t *testing.T) {
		conformPolicyCrossTenantIsolation(t, factory)
	})
	t.Run("Clone_GetByID_IsIndependent", func(t *testing.T) {
		conformPolicyCloneGetByIDIsIndependent(t, factory)
	})
	t.Run("Clone_ListByTenant_IsIndependent", func(t *testing.T) {
		conformPolicyCloneListByTenantIsIndependent(t, factory)
	})
	t.Run("Clone_FieldMask_IsIndependent", func(t *testing.T) {
		conformPolicyCloneFieldMaskIsIndependent(t, factory)
	})
	t.Run("InvalidTenant_Rejected", func(t *testing.T) {
		conformPolicyInvalidTenantRejected(t, factory)
	})
	t.Run("Concurrent_NoDataRace", func(t *testing.T) {
		conformPolicyConcurrentNoDataRace(t, factory)
	})
	t.Run("Create_NilPolicy_Error", func(t *testing.T) {
		conformPolicyCreateNilPolicyError(t, factory)
	})
	t.Run("CreateInputClone_IsIndependent", func(t *testing.T) {
		conformPolicyCreateInputCloneIsIndependent(t, factory)
	})
	t.Run("RepoReady_OK", func(t *testing.T) {
		conformPolicyRepoReady(t, factory)
	})
}

// conformPolicyRepoReady asserts that a freshly-constructed repository reports
// ready (RepoReady returns nil). mem is always ready; the PG store's policies
// table is reachable (and empty) in a fresh per-test database. This is the
// conformance complement to T8.4 — every PolicyRepository implementation feeds
// the cell-level readiness probe, so each must answer RepoReady on a healthy
// store.
func conformPolicyRepoReady(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	if err := repo.RepoReady(context.Background()); err != nil {
		t.Errorf("RepoReady() on a healthy repo must return nil, got %v", err)
	}
}

// conformTestPolicy builds a minimal valid Policy for conformance tests.
func conformTestPolicy(id string, tid tenant.TenantID) *abac.Policy {
	return &abac.Policy{
		ID:       id,
		TenantID: tid,
		Name:     "Conformance Policy " + id,
		Rules: []abac.Rule{
			{
				ID:     "rule-1",
				Name:   "Allow Rule",
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

// conformTestPolicyWithFieldMask builds a minimal valid Policy carrying a
// non-empty FieldMask obligation so the deep-copy path is exercised.
func conformTestPolicyWithFieldMask(id string, tid tenant.TenantID) *abac.Policy {
	p := conformTestPolicy(id, tid)
	p.Rules[0].Obligations.FieldMask.Fields = []string{"email", "phone"}
	return p
}

// mustCreate is a test helper that calls Create and fails the test on error.
func mustCreate(t *testing.T, repo ports.PolicyRepository, tid tenant.TenantID, p *abac.Policy) {
	t.Helper()
	if err := repo.Create(context.Background(), tid, p); err != nil {
		t.Fatalf(msgPolicyCreateUnexpected, err)
	}
}

func conformPolicyCreateAndGetByID(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicy("pol-1", testTenantID)

	mustCreate(t, repo, testTenantID, p)
	got, err := repo.GetByID(ctx, testTenantID, "pol-1")
	if err != nil {
		t.Fatalf(msgPolicyGetByIDUnexpected, err)
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

func conformPolicyGetByIDNotFound(t *testing.T, factory PolicyRepoFactory) error {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	_, err := repo.GetByID(ctx, testTenantID, "nonexistent")
	if err == nil {
		t.Fatal("GetByID() expected KindNotFound error, got nil")
	}
	assertPolicyKind(t, err, "GetByID() nonexistent")
	return err
}

func conformPolicyListByTenantEmpty(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	list, err := repo.ListByTenant(ctx, testTenantID)
	if err != nil {
		t.Fatalf(msgPolicyListByTenantUnexpected, err)
	}
	if list == nil {
		t.Error("ListByTenant() returned nil slice, want non-nil empty slice")
	}
	if len(list) != 0 {
		t.Errorf("ListByTenant() returned %d items, want 0", len(list))
	}
}

func conformPolicyListByTenant(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)

	p1 := conformTestPolicy("pol-a", testTenantID)
	p2 := conformTestPolicy("pol-b", testTenantID)
	for _, p := range []*abac.Policy{p1, p2} {
		mustCreate(t, repo, testTenantID, p)
	}

	list, err := repo.ListByTenant(context.Background(), testTenantID)
	if err != nil {
		t.Fatalf(msgPolicyListByTenantUnexpected, err)
	}
	if len(list) != 2 {
		t.Errorf("ListByTenant() returned %d items, want 2", len(list))
	}
}

// conformPolicyCreateConflict asserts that a second Create with the same id
// returns ErrAuthPolicyDuplicate (KindConflict).
func conformPolicyCreateConflict(t *testing.T, factory PolicyRepoFactory) error {
	t.Parallel()
	repo := factory(t)

	p := conformTestPolicy("pol-1", testTenantID)
	mustCreate(t, repo, testTenantID, p)

	// Second Create with same id must fail.
	err := repo.Create(context.Background(), testTenantID, conformTestPolicy("pol-1", testTenantID))
	if err == nil {
		t.Fatal("Create() second call with same id expected KindConflict, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindConflict {
		t.Errorf("Create() conflict: expected KindConflict, got %v", err)
	}
	return err
}

// conformPolicyUpdateCASSuccess verifies that Update with the correct
// expectedVersion succeeds, increments Version 1→2, and returns the updated
// aggregate.
func conformPolicyUpdateCASSuccess(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	p := conformTestPolicy("pol-1", testTenantID)
	mustCreate(t, repo, testTenantID, p)

	updated := conformTestPolicy("pol-1", testTenantID)
	updated.Name = "Updated Name"
	got, err := repo.Update(ctx, testTenantID, "pol-1", 1, updated)
	if err != nil {
		t.Fatalf("Update() unexpected error: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("Update() returned Version = %d, want 2", got.Version)
	}
	if got.Name != "Updated Name" {
		t.Errorf("Update() returned Name = %q, want %q", got.Name, "Updated Name")
	}

	// GetByID should reflect the new state.
	stored, err := repo.GetByID(ctx, testTenantID, "pol-1")
	if err != nil {
		t.Fatalf(msgPolicyGetByIDUnexpected, err)
	}
	if stored.Version != 2 {
		t.Errorf("GetByID() after Update Version = %d, want 2", stored.Version)
	}
	if stored.Name != "Updated Name" {
		t.Errorf("GetByID() after Update Name = %q, want %q", stored.Name, "Updated Name")
	}
}

// conformPolicyUpdateVersionConflict asserts that Update with the wrong
// expectedVersion returns ErrVersionConflict (KindConflict).
func conformPolicyUpdateVersionConflict(t *testing.T, factory PolicyRepoFactory) error {
	t.Parallel()
	repo := factory(t)

	p := conformTestPolicy("pol-1", testTenantID)
	mustCreate(t, repo, testTenantID, p)

	updated := conformTestPolicy("pol-1", testTenantID)
	updated.Name = "Should Not Stick"
	_, err := repo.Update(context.Background(), testTenantID, "pol-1", 99 /* wrong version */, updated)
	if err == nil {
		t.Fatal("Update() with wrong version expected KindConflict, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindConflict {
		t.Errorf("Update() version conflict: expected KindConflict, got %v", err)
	}
	return err
}

// conformPolicyUpdateNotFound asserts that Update on a non-existent id returns
// ErrAuthPolicyNotFound (KindNotFound).
func conformPolicyUpdateNotFound(t *testing.T, factory PolicyRepoFactory) error {
	t.Parallel()
	repo := factory(t)

	updated := conformTestPolicy("nonexistent", testTenantID)
	_, err := repo.Update(context.Background(), testTenantID, "nonexistent", 1, updated)
	if err == nil {
		t.Fatal("Update() on nonexistent id expected KindNotFound, got nil")
	}
	assertPolicyKind(t, err, "Update() nonexistent")
	return err
}

// conformPolicyDeleteCASSuccess verifies that Delete with the correct
// expectedVersion removes the policy and returns the deleted aggregate.
func conformPolicyDeleteCASSuccess(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicy("pol-1", testTenantID)

	mustCreate(t, repo, testTenantID, p)
	deleted, err := repo.Delete(ctx, testTenantID, "pol-1", 1)
	if err != nil {
		t.Fatalf("Delete() unexpected error: %v", err)
	}
	if deleted == nil {
		t.Fatal("Delete() returned nil policy, want the deleted aggregate")
	}
	if deleted.ID != "pol-1" {
		t.Errorf("Delete() returned ID = %q, want %q", deleted.ID, "pol-1")
	}
	if deleted.Version != 1 {
		t.Errorf("Delete() returned Version = %d, want 1", deleted.Version)
	}

	_, err = repo.GetByID(ctx, testTenantID, "pol-1")
	if err == nil {
		t.Fatal("GetByID() after Delete expected KindNotFound, got nil")
	}
	assertPolicyKind(t, err, "GetByID() after Delete")
}

// conformPolicyDeleteVersionConflict asserts that Delete with the wrong
// expectedVersion returns ErrVersionConflict (KindConflict).
func conformPolicyDeleteVersionConflict(t *testing.T, factory PolicyRepoFactory) error {
	t.Parallel()
	repo := factory(t)

	p := conformTestPolicy("pol-1", testTenantID)
	mustCreate(t, repo, testTenantID, p)

	_, err := repo.Delete(context.Background(), testTenantID, "pol-1", 99 /* wrong version */)
	if err == nil {
		t.Fatal("Delete() with wrong version expected KindConflict, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindConflict {
		t.Errorf("Delete() version conflict: expected KindConflict, got %v", err)
	}
	return err
}

func conformPolicyDeleteNotFound(t *testing.T, factory PolicyRepoFactory) error {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	_, err := repo.Delete(ctx, testTenantID, "nonexistent", 1)
	if err == nil {
		t.Fatal("Delete() nonexistent expected KindNotFound, got nil")
	}
	assertPolicyKind(t, err, "Delete() nonexistent")
	return err
}

func conformPolicyCrossTenantIsolation(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicy("pol-1", testTenantID)

	mustCreate(t, repo, testTenantID, p)

	// Other tenant must not see tenant 1's policy.
	_, err := repo.GetByID(ctx, testTenantIDOther, "pol-1")
	if err == nil {
		t.Fatal("GetByID() cross-tenant expected KindNotFound, got nil")
	}
	assertPolicyKind(t, err, "GetByID() cross-tenant")

	// ListByTenant for other tenant must be empty.
	list, err := repo.ListByTenant(ctx, testTenantIDOther)
	if err != nil {
		t.Fatalf("ListByTenant() cross-tenant unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListByTenant() cross-tenant returned %d items, want 0", len(list))
	}

	// Delete in other tenant must fail with KindNotFound.
	_, delErr := repo.Delete(ctx, testTenantIDOther, "pol-1", 1)
	if delErr == nil {
		t.Fatal("Delete() cross-tenant expected KindNotFound, got nil")
	} else {
		assertPolicyKind(t, delErr, "Delete() cross-tenant")
	}
}

func conformPolicyCloneGetByIDIsIndependent(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicy("pol-1", testTenantID)

	mustCreate(t, repo, testTenantID, p)
	got, err := repo.GetByID(ctx, testTenantID, "pol-1")
	if err != nil {
		t.Fatalf(msgPolicyGetByIDUnexpected, err)
	}

	// Mutate the returned clone.
	got.Name = "mutated"
	got.Rules[0].Name = "mutated rule"
	if len(got.Rules[0].Conditions) > 0 {
		got.Rules[0].Conditions[0].Key = "mutated_key"
		if len(got.Rules[0].Conditions[0].Values) > 0 {
			got.Rules[0].Conditions[0].Values[0] = "mutated_value"
		}
	}

	// Re-fetch must not reflect the mutations.
	got2, err := repo.GetByID(ctx, testTenantID, "pol-1")
	if err != nil {
		t.Fatalf("GetByID() second unexpected error: %v", err)
	}
	if got2.Name == "mutated" {
		t.Errorf("clone mutation leaked into store: Name = %q", got2.Name)
	}
	if got2.Rules[0].Name == "mutated rule" {
		t.Errorf("clone mutation leaked into store: Rules[0].Name = %q", got2.Rules[0].Name)
	}
}

func conformPolicyCloneListByTenantIsIndependent(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicy("pol-1", testTenantID)

	mustCreate(t, repo, testTenantID, p)
	list, err := repo.ListByTenant(ctx, testTenantID)
	if err != nil {
		t.Fatalf(msgPolicyListByTenantUnexpected, err)
	}
	if len(list) == 0 {
		t.Fatal("ListByTenant() returned empty list, want 1 item")
	}

	// Mutate the returned clone.
	list[0].Name = "mutated"

	// Re-list must not reflect the mutation.
	list2, err := repo.ListByTenant(ctx, testTenantID)
	if err != nil {
		t.Fatalf("ListByTenant() second unexpected error: %v", err)
	}
	if list2[0].Name == "mutated" {
		t.Errorf("clone mutation leaked into store: Name = %q", list2[0].Name)
	}
}

// conformPolicyCloneFieldMaskIsIndependent asserts that mutating
// Rules[i].Obligations.FieldMask.Fields on a returned clone does not affect the
// stored copy. This is the regression guard for the shallow-copy bug identified
// in PR-6 review finding F1.
func conformPolicyCloneFieldMaskIsIndependent(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicyWithFieldMask("pol-1", testTenantID)

	mustCreate(t, repo, testTenantID, p)
	got, err := repo.GetByID(ctx, testTenantID, "pol-1")
	if err != nil {
		t.Fatalf(msgPolicyGetByIDUnexpected, err)
	}
	if len(got.Rules[0].Obligations.FieldMask.Fields) == 0 {
		t.Fatal("GetByID() returned policy with no FieldMask.Fields; test fixture requires non-empty FieldMask")
	}

	// Mutate the slice element in the returned clone.
	got.Rules[0].Obligations.FieldMask.Fields[0] = "mutated_column"

	// Re-fetch must not reflect the mutation.
	got2, err := repo.GetByID(ctx, testTenantID, "pol-1")
	if err != nil {
		t.Fatalf("GetByID() second unexpected error: %v", err)
	}
	if len(got2.Rules[0].Obligations.FieldMask.Fields) == 0 {
		t.Fatal("second GetByID() returned policy with no FieldMask.Fields")
	}
	if got2.Rules[0].Obligations.FieldMask.Fields[0] == "mutated_column" {
		t.Errorf("FieldMask clone mutation leaked into store: Fields[0] = %q",
			got2.Rules[0].Obligations.FieldMask.Fields[0])
	}
}

func conformPolicyInvalidTenantRejected(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	invalid := tenant.TenantID("") // empty is invalid

	p := conformTestPolicy("pol-1", testTenantID)
	if err := repo.Create(ctx, invalid, p); err == nil {
		t.Error("Create() with empty TenantID expected error, got nil")
	}
	if _, err := repo.GetByID(ctx, invalid, "pol-1"); err == nil {
		t.Error("GetByID() with empty TenantID expected error, got nil")
	}
	if _, err := repo.ListByTenant(ctx, invalid); err == nil {
		t.Error("ListByTenant() with empty TenantID expected error, got nil")
	}
	if _, delErr2 := repo.Delete(ctx, invalid, "pol-1", 1); delErr2 == nil {
		t.Error("Delete() with empty TenantID expected error, got nil")
	}
}

// isNotFoundErr reports whether err is a *errcode.Error with KindNotFound.
func isNotFoundErr(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Kind == errcode.KindNotFound
}

// concurrentPolicyWorker performs one round of repo ops for the given worker
// index. Unexpected errors are appended to *unexpected under mu.
func concurrentPolicyWorker(
	ctx context.Context,
	repo ports.PolicyRepository,
	idx int,
	mu *sync.Mutex,
	unexpected *[]error,
) {
	const policyID = "pol-concurrent"
	p := conformTestPolicy(policyID, testTenantID)

	recordErr := func(err error) {
		mu.Lock()
		*unexpected = append(*unexpected, err)
		mu.Unlock()
	}

	_ = repo.Create(ctx, testTenantID, p) // conflict on duplicates is expected
	if _, err := repo.GetByID(ctx, testTenantID, policyID); err != nil {
		if !isNotFoundErr(err) {
			recordErr(err)
		}
	}
	if _, err := repo.ListByTenant(ctx, testTenantID); err != nil {
		recordErr(err)
	}
	if idx%2 == 0 {
		if _, err := repo.Delete(ctx, testTenantID, policyID, 1); err != nil {
			if !isNotFoundErr(err) && !isConflictErr(err) {
				recordErr(err)
			}
		}
	}
}

// conformPolicyConcurrentNoDataRace exercises concurrent Create / GetByID /
// ListByTenant / Delete operations against the same repo to surface data races
// when run with -race. The race detector is the primary assertion; unexpected
// (non-KindNotFound, non-KindConflict) errors are also collected and reported.
func conformPolicyConcurrentNoDataRace(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	const goroutines = 20
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		unexpected []error
	)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			concurrentPolicyWorker(ctx, repo, idx, &mu, &unexpected)
		}(i)
	}
	wg.Wait()

	if len(unexpected) > 0 {
		t.Errorf("concurrent ops produced %d unexpected error(s): first=%v", len(unexpected), unexpected[0])
	}
}

// conformPolicyCreateNilPolicyError asserts that Create with a nil *abac.Policy
// returns a non-nil KindInvalid error without panicking. This is the conformance
// complement to F7 (mem nil-guard) — PG and any future implementations inherit
// the same contract.
func conformPolicyCreateNilPolicyError(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	err := repo.Create(ctx, testTenantID, nil)
	if err == nil {
		t.Fatal("Create(ctx, t, nil) expected non-nil error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Errorf("Create(ctx, t, nil): expected *errcode.Error, got %T: %v", err, err)
		return
	}
	if ec.Kind != errcode.KindInvalid {
		t.Errorf("Create(ctx, t, nil): expected Kind KindInvalid, got %v (err: %v)", ec.Kind, err)
	}
}

// conformPolicyCreateInputCloneIsIndependent asserts that the stored policy is
// an independent deep copy of the value passed to Create: mutations made to the
// original after Create must not be visible via GetByID or ListByTenant.
func conformPolicyCreateInputCloneIsIndependent(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	p := conformTestPolicyWithFieldMask("pol-clone-input", testTenantID)

	origName := p.Name
	origCondVal := p.Rules[0].Conditions[0].Values[0]
	origField := p.Rules[0].Obligations.FieldMask.Fields[0]

	mustCreate(t, repo, testTenantID, p)

	// Mutate the ORIGINAL p after Create.
	p.Name = "mutated-after-create"
	p.Rules[0].Conditions[0].Values[0] = "mutated-value"
	p.Rules[0].Obligations.FieldMask.Fields[0] = "mutated_field"

	// GetByID must reflect the at-Create snapshot, not the mutations.
	got, err := repo.GetByID(ctx, testTenantID, "pol-clone-input")
	if err != nil {
		t.Fatalf(msgPolicyGetByIDUnexpected, err)
	}
	if got.Name != origName {
		t.Errorf("GetByID(): Name = %q, want %q (mutation of original leaked into store)", got.Name, origName)
	}
	if len(got.Rules) > 0 && len(got.Rules[0].Conditions) > 0 && len(got.Rules[0].Conditions[0].Values) > 0 {
		if got.Rules[0].Conditions[0].Values[0] != origCondVal {
			t.Errorf("GetByID(): Conditions[0].Values[0] = %q, want %q", got.Rules[0].Conditions[0].Values[0], origCondVal)
		}
	}
	if len(got.Rules) > 0 && len(got.Rules[0].Obligations.FieldMask.Fields) > 0 {
		if got.Rules[0].Obligations.FieldMask.Fields[0] != origField {
			t.Errorf("GetByID(): FieldMask.Fields[0] = %q, want %q", got.Rules[0].Obligations.FieldMask.Fields[0], origField)
		}
	}

	// ListByTenant must also reflect the at-Create snapshot.
	list, err := repo.ListByTenant(ctx, testTenantID)
	if err != nil {
		t.Fatalf(msgPolicyListByTenantUnexpected, err)
	}
	if len(list) == 0 {
		t.Fatal("ListByTenant() returned empty list, want 1 item")
	}
	if list[0].Name != origName {
		t.Errorf("ListByTenant(): Name = %q, want %q (mutation of original leaked into store)", list[0].Name, origName)
	}
}

// assertPolicyKind is a local helper that fails t if err is not a *errcode.Error
// with KindNotFound.
func assertPolicyKind(t *testing.T, err error, label string) {
	t.Helper()
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Errorf("%s: expected *errcode.Error, got %T: %v", label, err, err)
		return
	}
	if ec.Kind != errcode.KindNotFound {
		t.Errorf("%s: expected Kind KindNotFound, got %v (err: %v)", label, ec.Kind, err)
	}
}
