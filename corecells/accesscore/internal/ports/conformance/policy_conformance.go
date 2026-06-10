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
	msgPolicySaveUnexpected         = "Save() unexpected error: %v"
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
	t.Run("SaveAndGetByID_RoundTrip", func(t *testing.T) {
		conformPolicySaveAndGetByID(t, factory)
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
	t.Run("Delete_Succeeds", func(t *testing.T) {
		conformPolicyDeleteSucceeds(t, factory)
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
	t.Run("Save_NilPolicy_Error", func(t *testing.T) {
		conformPolicySaveNilPolicyError(t, factory)
	})
	t.Run("SaveInputClone_IsIndependent", func(t *testing.T) {
		conformPolicySaveInputCloneIsIndependent(t, factory)
	})
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

func conformPolicySaveAndGetByID(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicy("pol-1", testTenantID)

	if err := repo.Save(ctx, testTenantID, p); err != nil {
		t.Fatalf(msgPolicySaveUnexpected, err)
	}
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
	ctx := context.Background()

	p1 := conformTestPolicy("pol-a", testTenantID)
	p2 := conformTestPolicy("pol-b", testTenantID)
	for _, p := range []*abac.Policy{p1, p2} {
		if err := repo.Save(ctx, testTenantID, p); err != nil {
			t.Fatalf("Save(%s) unexpected error: %v", p.ID, err)
		}
	}

	list, err := repo.ListByTenant(ctx, testTenantID)
	if err != nil {
		t.Fatalf(msgPolicyListByTenantUnexpected, err)
	}
	if len(list) != 2 {
		t.Errorf("ListByTenant() returned %d items, want 2", len(list))
	}
}

func conformPolicyDeleteSucceeds(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicy("pol-1", testTenantID)

	if err := repo.Save(ctx, testTenantID, p); err != nil {
		t.Fatalf(msgPolicySaveUnexpected, err)
	}
	if err := repo.Delete(ctx, testTenantID, "pol-1"); err != nil {
		t.Fatalf("Delete() unexpected error: %v", err)
	}
	_, err := repo.GetByID(ctx, testTenantID, "pol-1")
	if err == nil {
		t.Fatal("GetByID() after Delete expected KindNotFound, got nil")
	}
	assertPolicyKind(t, err, "GetByID() after Delete")
}

func conformPolicyDeleteNotFound(t *testing.T, factory PolicyRepoFactory) error {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	err := repo.Delete(ctx, testTenantID, "nonexistent")
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

	if err := repo.Save(ctx, testTenantID, p); err != nil {
		t.Fatalf(msgPolicySaveUnexpected, err)
	}

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
	if err := repo.Delete(ctx, testTenantIDOther, "pol-1"); err == nil {
		t.Fatal("Delete() cross-tenant expected KindNotFound, got nil")
	} else {
		assertPolicyKind(t, err, "Delete() cross-tenant")
	}
}

func conformPolicyCloneGetByIDIsIndependent(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()
	p := conformTestPolicy("pol-1", testTenantID)

	if err := repo.Save(ctx, testTenantID, p); err != nil {
		t.Fatalf(msgPolicySaveUnexpected, err)
	}
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

	if err := repo.Save(ctx, testTenantID, p); err != nil {
		t.Fatalf(msgPolicySaveUnexpected, err)
	}
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

	if err := repo.Save(ctx, testTenantID, p); err != nil {
		t.Fatalf(msgPolicySaveUnexpected, err)
	}
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
	if err := repo.Save(ctx, invalid, p); err == nil {
		t.Error("Save() with empty TenantID expected error, got nil")
	}
	if _, err := repo.GetByID(ctx, invalid, "pol-1"); err == nil {
		t.Error("GetByID() with empty TenantID expected error, got nil")
	}
	if _, err := repo.ListByTenant(ctx, invalid); err == nil {
		t.Error("ListByTenant() with empty TenantID expected error, got nil")
	}
	if err := repo.Delete(ctx, invalid, "pol-1"); err == nil {
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

	if err := repo.Save(ctx, testTenantID, p); err != nil {
		recordErr(err)
	}
	if _, err := repo.GetByID(ctx, testTenantID, policyID); err != nil {
		if !isNotFoundErr(err) {
			recordErr(err)
		}
	}
	if _, err := repo.ListByTenant(ctx, testTenantID); err != nil {
		recordErr(err)
	}
	if idx%2 == 0 {
		if err := repo.Delete(ctx, testTenantID, policyID); err != nil {
			if !isNotFoundErr(err) {
				recordErr(err)
			}
		}
	}
}

// conformPolicyConcurrentNoDataRace exercises concurrent Save / GetByID /
// ListByTenant / Delete operations against the same repo to surface data races
// when run with -race. The race detector is the primary assertion; unexpected
// (non-KindNotFound) errors are also collected and reported.
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

// conformPolicySaveNilPolicyError asserts that Save with a nil *abac.Policy
// returns a non-nil KindInvalid error without panicking. This is the conformance
// complement to F7 (mem nil-guard) — PG and any future implementations inherit
// the same contract.
func conformPolicySaveNilPolicyError(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	err := repo.Save(ctx, testTenantID, nil)
	if err == nil {
		t.Fatal("Save(ctx, t, nil) expected non-nil error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Errorf("Save(ctx, t, nil): expected *errcode.Error, got %T: %v", err, err)
		return
	}
	if ec.Kind != errcode.KindInvalid {
		t.Errorf("Save(ctx, t, nil): expected Kind KindInvalid, got %v (err: %v)", ec.Kind, err)
	}
}

// conformPolicySaveInputCloneIsIndependent asserts that the stored policy is an
// independent deep copy of the value passed to Save: mutations made to the
// original after Save must not be visible via GetByID or ListByTenant.
//
// This is the "Save-input snapshot boundary" test required by F8.
func conformPolicySaveInputCloneIsIndependent(t *testing.T, factory PolicyRepoFactory) {
	t.Parallel()
	repo := factory(t)
	ctx := context.Background()

	// Build a policy with nested Rules → Conditions (with Values) and
	// Obligations.FieldMask.Fields — exercising every deep-copy branch.
	p := conformTestPolicyWithFieldMask("pol-clone-input", testTenantID)

	// Take a snapshot of all fields we will mutate afterwards.
	origName := p.Name
	origCondVal := p.Rules[0].Conditions[0].Values[0]
	origField := p.Rules[0].Obligations.FieldMask.Fields[0]

	if err := repo.Save(ctx, testTenantID, p); err != nil {
		t.Fatalf(msgPolicySaveUnexpected, err)
	}

	// Mutate the ORIGINAL p after Save.
	p.Name = "mutated-after-save"
	p.Rules[0].Conditions[0].Values[0] = "mutated-value"
	p.Rules[0].Obligations.FieldMask.Fields[0] = "mutated_field"

	// GetByID must reflect the at-Save snapshot, not the mutations.
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

	// ListByTenant must also reflect the at-Save snapshot.
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
