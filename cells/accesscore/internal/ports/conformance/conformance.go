// Package conformance defines a UserRepository contract acceptance suite shared
// by all ports.UserRepository implementations (mem, PG, future). Each implementation
// MUST call RunUserRepoConformance from a _test.go in its own package; the archtest
// USERREPO-CONFORMANCE-ENROLLMENT-01 enforces enrollment.
//
// ref: runtime/distlock/locktest/conformance.go (Factory + Features branch — not t.Skip)
// ref: ThreeDotsLabs/watermill pubsub/tests/test_pubsub.go (Features bool struct)
package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
)

// concurrencyDeadlineBudget bounds the Concurrent_NoDeadlock sub-test so a
// hung implementation surfaces as a deadline-exceeded failure rather than
// hanging the whole `go test` invocation. Extracted to a const per
// TEST-TIME-LITERAL-01 (file-local site-specific deadline; no cross-cutting
// reuse — kept in this file).
const concurrencyDeadlineBudget = 30 * time.Second

// quickGracePeriod is the small wall-time used to differentiate "immediately
// returned" from "blocked on lock" in conformGetByIDForUpdateLockContention.
// 50ms is large enough to avoid scheduler-noise false positives on typical CI
// hardware, small enough to not balloon the suite runtime.
const quickGracePeriod = 50 * time.Millisecond

// holderTimeout bounds the holder and contender goroutines in
// conformGetByIDForUpdateLockContention so a wedge surfaces as a test failure
// rather than hanging the whole `go test` invocation.
const holderTimeout = 3 * time.Second

// deadlockGracePeriod is the small extra wall-time allowed for
// conformConcurrentNoDeadlock's goroutines to observe ctx.Done and unwind
// after the context deadline fires. Without this watchdog grace, a real
// deadlock would block wg.Wait() until the `go test -timeout` global limit.
const deadlockGracePeriod = 2 * time.Second

// Auto-lockout conformance fixture gaps. Stale window for the test seed mirrors
// the production cells/accesscore/internal/accountlockout policy (StaleWindow
// = LockoutTTL = 15 minutes), kept as package-level consts to satisfy
// TEST-TIME-LITERAL-01.
const (
	lockoutFixtureFailedGap   = -2 * time.Minute
	lockoutFixtureLockedUntil = 15 * time.Minute
)

// exampleEmailDomain is the synthetic email suffix appended to generated
// usernames across conformance fixtures (no real mailbox; uniqueness comes
// from the username/UUID prefix).
const exampleEmailDomain = "@example.com"

// testTenantID is the canonical test tenant UUID used by all conformance
// sub-tests. It scopes every repo read/write to the same tenant partition,
// mirroring production callers that derive it from context.
var testTenantID = func() tenant.TenantID {
	t, err := tenant.ParseTenantID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		panic(panicregister.Approved("conformance-test-tenant-id",
			errcode.Assertion("conformance: invalid testTenantID: %v", err)))
	}
	return t
}()

// testTenantIDOther is a second canonical tenant UUID used by the cross-tenant
// isolation sub-test (CrossTenant_Isolation) to assert that rows created under
// testTenantID are invisible to reads/writes scoped to a different tenant.
var testTenantIDOther = func() tenant.TenantID {
	t, err := tenant.ParseTenantID("00000000-0000-0000-0000-000000000002")
	if err != nil {
		panic(panicregister.Approved("conformance-test-tenant-id-other",
			errcode.Assertion("conformance: invalid testTenantIDOther: %v", err)))
	}
	return t
}()

// UserRepoFactory constructs a fresh ports.UserRepository, its paired
// persistence.TxRunner, and a cleanup func for use in a single test sub-case.
// The factory is called once per sub-test; the cleanup func is registered via
// t.Cleanup before the sub-test body runs.
type UserRepoFactory func(t *testing.T) (
	repo ports.UserRepository,
	txRunner persistence.TxRunner,
	cleanup func(),
)

// Features describes which optional behaviors the implementation under test
// supports. Assertions branch on these flags rather than using t.Skip, so every
// sub-test always exercises a concrete code path (no silent skip).
type Features struct {
	// RequiresAmbientTx: GetByIDForUpdate / GetByUsernameForUpdate WITHOUT a
	// persistence ambient tx returns errcode.ErrInternal. PG=true, mem=false.
	RequiresAmbientTx bool

	// SupportsCASConflict: concurrent UpdatePassword / BumpAuthzEpoch returns
	// errcode.ErrConflict-family on the loser. PG=true (version column CAS),
	// mem=false (store.mu serializes all writes; no lost update; no conflict).
	//
	// For mem: the conformance suite exercises the deterministic stale-version
	// path only (no concurrent goroutines). The concurrent-race regression — where
	// exactly one of two concurrent ChangePassword calls succeeds — is covered by
	// cells/accesscore/slices/identitymanage.TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds
	// (live goroutine test, ADR 202605171846 §D4); that test runs against the
	// mem Store's own TxRunner and validates the serialization guarantee end-to-end.
	SupportsCASConflict bool
}

// RunUserRepoConformance executes the full UserRepository conformance suite.
func RunUserRepoConformance(t *testing.T, factory UserRepoFactory, features Features) {
	t.Helper()
	t.Run("GetByIDForUpdate_NoTx", func(t *testing.T) {
		conformGetByIDForUpdateNoTx(t, factory, features)
	})
	t.Run("GetByIDForUpdate_WithTx_Succeeds", func(t *testing.T) {
		conformGetByIDForUpdateWithTx(t, factory)
	})
	t.Run("GetByUsernameForUpdate_NoTx", func(t *testing.T) {
		conformGetByUsernameForUpdateNoTx(t, factory, features)
	})
	t.Run("GetByUsernameForUpdate_WithTx_Succeeds", func(t *testing.T) {
		conformGetByUsernameForUpdateWithTx(t, factory)
	})
	t.Run("UpdatePassword_Succeeds", func(t *testing.T) {
		conformUpdatePasswordSucceeds(t, factory)
	})
	t.Run("UpdatePassword_CASConflict", func(t *testing.T) {
		conformUpdatePasswordCASConflict(t, factory, features)
	})
	t.Run("UpdatePassword_InactiveRejected", func(t *testing.T) {
		conformUpdatePasswordInactiveRejected(t, factory)
	})
	t.Run("BumpAuthzEpoch_Succeeds", func(t *testing.T) {
		conformBumpAuthzEpochSucceeds(t, factory)
	})
	t.Run("BumpAuthzEpoch_MonotonicIncrement", func(t *testing.T) {
		conformBumpAuthzEpochMonotonic(t, factory)
	})
	t.Run("BumpAuthzEpoch_NilFenceToken_Panics", func(t *testing.T) {
		conformBumpAuthzEpochNilFenceToken(t, factory)
	})
	t.Run("GetByIDForUpdate_LockContention", func(t *testing.T) {
		conformGetByIDForUpdateLockContention(t, factory)
	})
	t.Run("NotFound_PropagatesErrAuthUserNotFound", func(t *testing.T) {
		conformNotFoundPropagates(t, factory)
	})
	t.Run("CrossTenant_Isolation", func(t *testing.T) {
		conformCrossTenantIsolation(t, factory, features)
	})
	t.Run("Concurrent_NoDeadlock", func(t *testing.T) {
		conformConcurrentNoDeadlock(t, factory)
	})
	// F22: UpdateLockoutFields contract — persists counter / lastFailedAt /
	// lockedUntil without touching status / epoch / password columns.
	t.Run("UpdateLockoutFields_Succeeds", func(t *testing.T) {
		conformUpdateLockoutFieldsSucceeds(t, factory)
	})
	t.Run("UpdateLockoutFields_NotFound", func(t *testing.T) {
		err := conformUpdateLockoutFieldsNotFound(t, factory)
		// POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01: typed funnel
		// must be inline at the test site (archtest does not follow helpers).
		errcodetest.AssertCode(t, err, errcode.ErrAuthUserNotFound)
	})
	runNarrowWriteSurfaceConformance(t, factory)
}

// runNarrowWriteSurfaceConformance covers USERREPO-METHOD-SET-FROZEN-01
// (issue #828): generic Update(*User) is removed. Each narrow method touches
// a disjoint column set; the sub-tests below pin column isolation in mem + PG
// so a regression that re-introduces field bleed surfaces here, not in
// production.
func runNarrowWriteSurfaceConformance(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	t.Run("UpdateProfile_Succeeds", func(t *testing.T) {
		conformUpdateProfileSucceeds(t, factory)
	})
	t.Run("UpdateProfile_PartialPATCH", func(t *testing.T) {
		conformUpdateProfilePartialPATCH(t, factory)
	})
	t.Run("UpdateProfile_NotFound", func(t *testing.T) {
		err := conformUpdateProfileNotFound(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthUserNotFound)
	})
	t.Run("UpdateProfile_DuplicateUsername", func(t *testing.T) {
		err := conformUpdateProfileDuplicateUsername(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthUserDuplicate)
	})
	t.Run("UpdateProfile_DuplicateEmail", func(t *testing.T) {
		err := conformUpdateProfileDuplicateEmail(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthUserDuplicate)
	})
	t.Run("UpdateProfile_SameValuesNoOp", func(t *testing.T) {
		conformUpdateProfileSameValuesNoOp(t, factory)
	})
	t.Run("UpdateLockState_Succeeds", func(t *testing.T) {
		conformUpdateLockStateSucceeds(t, factory)
	})
	t.Run("UpdateLockState_ActivateClearsLockout", func(t *testing.T) {
		conformUpdateLockStateActivateClearsLockout(t, factory)
	})
	t.Run("UpdateLockState_NotFound", func(t *testing.T) {
		err := conformUpdateLockStateNotFound(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthUserNotFound)
	})
	t.Run("UpdatePasswordResetFlag_Succeeds", func(t *testing.T) {
		conformUpdatePasswordResetFlagSucceeds(t, factory)
	})
	t.Run("UpdatePasswordResetFlag_NotFound", func(t *testing.T) {
		err := conformUpdatePasswordResetFlagNotFound(t, factory)
		errcodetest.AssertCode(t, err, errcode.ErrAuthUserNotFound)
	})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// nePtr is a test-side bridge from a known-non-empty string literal to a
// *domain.NonEmpty. Conformance test values are seeded with valid usernames /
// emails, so the empty check inside NewNonEmpty is redundant noise here —
// direct cast is allowlisted for *_test.go by USERREPO-NONEMPTY-CAST-FUNNEL-01.
func nePtr(s string) *domain.NonEmpty {
	n := domain.NonEmpty(s)
	return &n
}

// seedActive creates and persists an active user (in testTenantID) inside a
// RunInTx call. It uses unique IDs so parallel sub-tests don't collide.
func seedActive(t *testing.T, txRunner persistence.TxRunner, repo ports.UserRepository, id, username string) *domain.User {
	t.Helper()
	return seedActiveInTenant(t, txRunner, repo, testTenantID, id, username)
}

// seedActiveInTenant is seedActive scoped to an explicit tenant — used by the
// cross-tenant isolation sub-test to stage a row in one tenant and probe it
// from another.
func seedActiveInTenant(
	t *testing.T, txRunner persistence.TxRunner, repo ports.UserRepository, tid tenant.TenantID, id, username string,
) *domain.User {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{ //nolint:gosec // test helper, not real credentials
		ID:           id,
		Username:     username,
		Email:        username + exampleEmailDomain,
		PasswordHash: "$2a$12$conformancefakehash",
		Status:       domain.StatusActive,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("seedActiveInTenant: ReconstituteUser: %v", err)
	}
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		return repo.Create(ctx, tid, u)
	}); err != nil {
		t.Fatalf("seedActiveInTenant: Create: %v", err)
	}
	return u
}

// isErrAuthUserNotFound reports whether err carries errcode.ErrAuthUserNotFound.
func isErrAuthUserNotFound(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Code == errcode.ErrAuthUserNotFound
}

// isErrInternal reports whether err carries a KindInternal error.
func isErrInternal(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Kind == errcode.KindInternal
}

// isConflictErr reports whether err is a CAS/version conflict (KindConflict).
func isConflictErr(err error) bool {
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Kind == errcode.KindConflict
}

// ─── sub-tests ────────────────────────────────────────────────────────────────

// conformGetByIDForUpdateNoTx: without ambient tx:
//   - PG (RequiresAmbientTx=true): returns ErrInternal (KindInternal) — the
//     assertion fires before any row lookup, so the envelope is intentionally
//     distinct from ErrAuthUserNotFound to prevent caller probing for IDs
//     via no-tx invocations.
//   - mem (RequiresAmbientTx=false): returns user (per-call lock)
func conformGetByIDForUpdateNoTx(t *testing.T, factory UserRepoFactory, features Features) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "noTxID_"+uuid.NewString())

	_, err := repo.GetByIDForUpdate(context.Background(), testTenantID, u.ID)
	if features.RequiresAmbientTx {
		if err == nil {
			t.Fatal("GetByIDForUpdate_NoTx: PG must return error when no ambient tx, got nil")
		}
		if !isErrInternal(err) {
			t.Errorf("GetByIDForUpdate_NoTx: error must be KindInternal, got %v", err)
		}
	} else if err != nil {
		t.Errorf("GetByIDForUpdate_NoTx: mem must succeed without ambient tx, got %v", err)
	}
}

// conformGetByIDForUpdateWithTx: inside RunInTx both mem and PG return the user.
func conformGetByIDForUpdateWithTx(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "withTxID_"+uuid.NewString())

	var got *domain.User
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		got, err = repo.GetByIDForUpdate(ctx, testTenantID, u.ID)
		return err
	}); err != nil {
		t.Fatalf("GetByIDForUpdate_WithTx: RunInTx: %v", err)
	}
	if got == nil {
		t.Fatal("GetByIDForUpdate_WithTx: expected non-nil user")
	}
	if got.ID != u.ID {
		t.Errorf("GetByIDForUpdate_WithTx: got ID %q, want %q", got.ID, u.ID)
	}
}

// conformGetByUsernameForUpdateNoTx: parallel to conformGetByIDForUpdateNoTx.
//
// PG no-ambient-tx returns ErrInternal (KindInternal) rather than
// ErrAuthUserNotFound: SELECT...FOR UPDATE must be issued inside an explicit
// tx, and the assertion fires before any row lookup. The error envelope is
// intentionally distinct from "user not found" so callers cannot use no-tx
// invocations to probe for username existence.
func conformGetByUsernameForUpdateNoTx(t *testing.T, factory UserRepoFactory, features Features) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "noTxUN_"+uuid.NewString())

	_, err := repo.GetByUsernameForUpdate(context.Background(), testTenantID, u.Username)
	if features.RequiresAmbientTx {
		if err == nil {
			t.Fatal("GetByUsernameForUpdate_NoTx: PG must return error when no ambient tx, got nil")
		}
		if !isErrInternal(err) {
			t.Errorf("GetByUsernameForUpdate_NoTx: error must be KindInternal, got %v", err)
		}
	} else if err != nil {
		t.Errorf("GetByUsernameForUpdate_NoTx: mem must succeed without ambient tx, got %v", err)
	}
}

// conformGetByUsernameForUpdateWithTx: inside RunInTx both implementations succeed.
func conformGetByUsernameForUpdateWithTx(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "withTxUN_"+uuid.NewString())

	var got *domain.User
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		got, err = repo.GetByUsernameForUpdate(ctx, testTenantID, u.Username)
		return err
	}); err != nil {
		t.Fatalf("GetByUsernameForUpdate_WithTx: RunInTx: %v", err)
	}
	if got == nil {
		t.Fatal("GetByUsernameForUpdate_WithTx: expected non-nil user")
	}
	if got.Username != u.Username {
		t.Errorf("GetByUsernameForUpdate_WithTx: got username %q, want %q", got.Username, u.Username)
	}
}

// conformUpdatePasswordSucceeds: successful CAS update returns version+1.
func conformUpdatePasswordSucceeds(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "pwdOK_"+uuid.NewString())
	initialVersion := u.PasswordVersion // 0

	newVersion, err := repo.UpdatePassword(
		context.Background(), testTenantID, u.ID, "$2a$12$newhash", false, initialVersion,
	)
	if err != nil {
		t.Fatalf("UpdatePassword_Succeeds: %v", err)
	}
	if newVersion != initialVersion+1 {
		t.Errorf("UpdatePassword_Succeeds: want version %d, got %d", initialVersion+1, newVersion)
	}
}

// conformUpdatePasswordInactiveRejected verifies the #1017 F1 write-time status
// guard: UpdatePassword on a frozen (suspended/locked) account is rejected with
// ErrAuthUserNotActive and does NOT rewrite the credential. This is the backstop
// for a concurrent Lock/Suspend committing between the caller's read and write.
func conformUpdatePasswordInactiveRejected(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "pwdInactive_"+uuid.NewString())

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := repo.UpdateLockState(context.Background(), testTenantID, u.ID, domain.StatusSuspended, now); err != nil {
		t.Fatalf("UpdatePassword_InactiveRejected: UpdateLockState: %v", err)
	}

	// expectedPV matches (0) — the status guard must fire BEFORE the version
	// guard, so the result is ErrAuthUserNotActive, not a CAS conflict.
	_, err := repo.UpdatePassword(context.Background(), testTenantID, u.ID, "$2a$12$newhashafterfreeze", false, u.PasswordVersion)
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Code != errcode.ErrAuthUserNotActive {
		t.Fatalf("UpdatePassword_InactiveRejected: want ErrAuthUserNotActive, got %v", err)
	}

	got, gerr := repo.GetByID(context.Background(), u.ID)
	if gerr != nil {
		t.Fatalf("UpdatePassword_InactiveRejected: GetByID: %v", gerr)
	}
	if got.PasswordHash != u.PasswordHash {
		t.Error("UpdatePassword_InactiveRejected: password_hash must be unchanged on a frozen account")
	}
	if got.PasswordVersion != u.PasswordVersion {
		t.Errorf("UpdatePassword_InactiveRejected: password_version must not advance: got %d, want %d",
			got.PasswordVersion, u.PasswordVersion)
	}
}

// conformUpdatePasswordCASConflict verifies CAS behavior:
//   - mem (SupportsCASConflict=false): serial calls with stale version return KindConflict
//   - PG (SupportsCASConflict=true): concurrent calls — one succeeds, one conflicts
func conformUpdatePasswordCASConflict(t *testing.T, factory UserRepoFactory, features Features) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "pwdCAS_"+uuid.NewString())

	if !features.SupportsCASConflict {
		// mem: first update bumps 0→1, second with stale version=0 must conflict.
		_, err1 := repo.UpdatePassword(context.Background(), testTenantID, u.ID, "$2a$12$hash1", false, 0)
		if err1 != nil {
			t.Fatalf("UpdatePassword_CASConflict/mem: first update failed: %v", err1)
		}

		_, err2 := repo.UpdatePassword(context.Background(), testTenantID, u.ID, "$2a$12$hash2", false, 0)
		if err2 == nil {
			t.Fatal("UpdatePassword_CASConflict/mem: second update with stale version must fail")
		}
		if !isConflictErr(err2) {
			t.Errorf("UpdatePassword_CASConflict/mem: stale-version error must be KindConflict, got %v", err2)
		}
		return
	}

	// PG: concurrent updates — exactly one winner, one loser.
	const goroutines = 2
	type result struct {
		newVersion int64
		err        error
	}
	results := make([]result, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			nv, err := repo.UpdatePassword(
				context.Background(), testTenantID, u.ID,
				fmt.Sprintf("$2a$12$concurrent%d", idx), false,
				0, // both read stale version=0
			)
			results[idx] = result{newVersion: nv, err: err}
		}(i)
	}
	wg.Wait()

	successCount, conflictCount := 0, 0
	for _, r := range results {
		switch {
		case r.err == nil:
			successCount++
		case isConflictErr(r.err):
			conflictCount++
		default:
			t.Errorf("UpdatePassword_CASConflict/PG: unexpected error: %v", r.err)
		}
	}
	if successCount != 1 {
		t.Errorf("UpdatePassword_CASConflict/PG: want 1 success, got %d", successCount)
	}
	if conflictCount != 1 {
		t.Errorf("UpdatePassword_CASConflict/PG: want 1 conflict, got %d", conflictCount)
	}
}

// conformBumpAuthzEpochSucceeds: BumpAuthzEpoch inside tx returns epoch+1.
func conformBumpAuthzEpochSucceeds(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "epochOK_"+uuid.NewString())
	initialEpoch := u.AuthzEpoch() // 1

	var newEpoch int64
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		newEpoch, err = repo.BumpAuthzEpoch(ctx, testTenantID, u.ID, credentialfence.Mint())
		return err
	}); err != nil {
		t.Fatalf("BumpAuthzEpoch_Succeeds: %v", err)
	}
	if newEpoch != initialEpoch+1 {
		t.Errorf("BumpAuthzEpoch_Succeeds: want epoch %d, got %d", initialEpoch+1, newEpoch)
	}
}

// conformBumpAuthzEpochNilFenceToken: a nil FenceToken is a programmer error
// that credentialfence.MustHave converts to a B-class panic (*errcode.Error,
// KindInternal) identifying the call site. Every UserRepository impl (mem / PG)
// must honor the guard — the shared conformance suite holds them all to it
// rather than relying on per-impl unit tests. MustHave is the first statement
// of each impl, so the panic fires before any tx / backend I/O (no seed or
// RunInTx needed).
func conformBumpAuthzEpochNilFenceToken(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		// nil FenceToken is intentional — exercising the MustHave guard.
		_, _ = repo.BumpAuthzEpoch(context.Background(), testTenantID, "usr-nil-token", nil)
	}()

	if recovered == nil {
		t.Fatal("nil FenceToken must trigger MustHave panic, got nil")
	}
	coded, ok := recovered.(*errcode.Error)
	if !ok {
		t.Fatalf("panic payload must be *errcode.Error, got %T: %v", recovered, recovered)
	}
	if coded.Kind != errcode.KindInternal {
		t.Errorf("nil-token panic must carry KindInternal (Assertion), got %v", coded.Kind)
	}
	if !strings.Contains(coded.Message, "ports.UserRepository.BumpAuthzEpoch") {
		t.Errorf("panic message must identify call site, got %q", coded.Message)
	}
}

// conformBumpAuthzEpochMonotonic verifies monotonic epoch increments.
//
// BumpAuthzEpoch is a monotonic increment with no caller-supplied expected value,
// so concurrent calls cannot produce a KindConflict. The test verifies that two
// sequential bumps both succeed and produce strictly increasing epoch values.
func conformBumpAuthzEpochMonotonic(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "epochMono_"+uuid.NewString())

	var epoch1 int64
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		epoch1, err = repo.BumpAuthzEpoch(ctx, testTenantID, u.ID, credentialfence.Mint())
		return err
	}); err != nil {
		t.Fatalf("BumpAuthzEpoch_MonotonicIncrement: first bump: %v", err)
	}

	var epoch2 int64
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		epoch2, err = repo.BumpAuthzEpoch(ctx, testTenantID, u.ID, credentialfence.Mint())
		return err
	}); err != nil {
		t.Fatalf("BumpAuthzEpoch_MonotonicIncrement: second bump: %v", err)
	}

	if epoch2 <= epoch1 {
		t.Errorf("BumpAuthzEpoch_MonotonicIncrement: second bump (%d) must be > first bump (%d)", epoch2, epoch1)
	}
}

// conformNotFoundPropagates: GetByID / GetByUsername on unknown IDs return ErrAuthUserNotFound.
func conformNotFoundPropagates(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	phantom := uuid.NewString()

	_, err := repo.GetByID(context.Background(), phantom)
	if err == nil {
		t.Fatal("NotFound: GetByID on unknown ID must return error, got nil")
	}
	if !isErrAuthUserNotFound(err) {
		t.Errorf("NotFound: GetByID must return ErrAuthUserNotFound, got %v", err)
	}

	_, err = repo.GetByUsername(context.Background(), testTenantID, "nobody_"+phantom)
	if err == nil {
		t.Fatal("NotFound: GetByUsername on unknown username must return error, got nil")
	}
	if !isErrAuthUserNotFound(err) {
		t.Errorf("NotFound: GetByUsername must return ErrAuthUserNotFound, got %v", err)
	}
}

// conformCrossTenantIsolation asserts the #1337 PR-2a T2.6 acceptance
// ("跨租户查询返回空"): a user created under tenant A is invisible to
// tenant-scoped reads/writes issued under a different tenant B, while remaining
// visible under its own tenant. This is the behavioral backstop for the mem
// tenant-membership guard and the PG `WHERE tenant_id = $N` predicate.
//
// GetByID is intentionally EXCLUDED: it is the by-global-UUID-PK tenant-deriving
// carve-out (returns the row regardless of tenant, by design — sessionrefresh
// has no pre-auth tenant); DB-layer RLS is its cross-tenant backstop in PR-3.
func conformCrossTenantIsolation(t *testing.T, factory UserRepoFactory, features Features) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	id := uuid.NewString()
	username := "xtenant_" + uuid.NewString()
	seedActiveInTenant(t, txRunner, repo, testTenantID, id, username)
	ctx := context.Background()

	// by-attribute read under tenant B → not found.
	if _, err := repo.GetByUsername(ctx, testTenantIDOther, username); !isErrAuthUserNotFound(err) {
		t.Fatalf("GetByUsername cross-tenant: want ErrAuthUserNotFound, got %v", err)
	}

	// narrow-write methods under tenant B → not found (predicate rejects the row).
	conformCrossTenantWriteProbes(t, repo, id, username)

	// for-update reads + BumpAuthzEpoch under tenant B → not found. Gate on ambient tx (PG).
	probeForUpdate := func(ctx context.Context) error {
		if _, err := repo.GetByIDForUpdate(ctx, testTenantIDOther, id); !isErrAuthUserNotFound(err) {
			return fmt.Errorf("GetByIDForUpdate cross-tenant: want ErrAuthUserNotFound, got %w", err)
		}
		if _, err := repo.GetByUsernameForUpdate(ctx, testTenantIDOther, username); !isErrAuthUserNotFound(err) {
			return fmt.Errorf("GetByUsernameForUpdate cross-tenant: want ErrAuthUserNotFound, got %w", err)
		}
		// BumpAuthzEpoch cross-tenant: tenant predicate must prevent the bump.
		if _, err := repo.BumpAuthzEpoch(ctx, testTenantIDOther, id, credentialfence.Mint()); !isErrAuthUserNotFound(err) {
			return fmt.Errorf("BumpAuthzEpoch cross-tenant: want ErrAuthUserNotFound, got %w", err)
		}
		return nil
	}
	if features.RequiresAmbientTx {
		if err := txRunner.RunInTx(ctx, probeForUpdate); err != nil {
			t.Fatal(err)
		}
	} else if err := probeForUpdate(ctx); err != nil {
		t.Fatal(err)
	}

	// Sanity: the row IS visible under its own tenant, so the rejections above
	// are genuinely tenant-scoped and not a seeding artifact.
	if _, err := repo.GetByUsername(ctx, testTenantID, username); err != nil {
		t.Fatalf("GetByUsername same-tenant: want success, got %v", err)
	}
}

// conformCrossTenantWriteProbes asserts that the non-tx-gated narrow-write
// methods (UpdateProfile / UpdateLockState / UpdatePassword / UpdatePasswordResetFlag /
// UpdateLockoutFields) return ErrAuthUserNotFound when called under the wrong
// tenant. Extracted from conformCrossTenantIsolation to keep that function
// within the cognitive-complexity budget (≤15).
func conformCrossTenantWriteProbes(t *testing.T, repo ports.UserRepository, id, username string) {
	t.Helper()
	ctx := context.Background()
	// UpdateProfile cross-tenant.
	if _, err := repo.UpdateProfile(ctx, testTenantIDOther, id, nePtr("hijack"), nil, time.Now().UTC()); !isErrAuthUserNotFound(err) {
		t.Fatalf("UpdateProfile cross-tenant: want ErrAuthUserNotFound, got %v", err)
	}
	// UpdateLockState cross-tenant.
	if err := repo.UpdateLockState(ctx, testTenantIDOther, id, domain.StatusLocked, time.Now().UTC()); !isErrAuthUserNotFound(err) {
		t.Fatalf("UpdateLockState cross-tenant: want ErrAuthUserNotFound, got %v", err)
	}
	// UpdatePassword cross-tenant: stale version (0) is fine — tenant predicate fires first.
	if _, err := repo.UpdatePassword(ctx, testTenantIDOther, id, "$2a$12$xthash", false, 0); !isErrAuthUserNotFound(err) { //nolint:lll // line length
		t.Fatalf("UpdatePassword cross-tenant: want ErrAuthUserNotFound, got %v", err)
	}
	// UpdatePasswordResetFlag cross-tenant.
	if err := repo.UpdatePasswordResetFlag(ctx, testTenantIDOther, id, true, time.Now().UTC()); !isErrAuthUserNotFound(err) {
		t.Fatalf("UpdatePasswordResetFlag cross-tenant: want ErrAuthUserNotFound, got %v", err)
	}
	// UpdateLockoutFields cross-tenant: build a minimal domain.User with the seeded id.
	crossTenantUser, reconErr := domain.ReconstituteUser(
		domain.ReconstituteUserParams{ //nolint:gosec // G101: test constant, not real credentials
			ID:           id,
			Username:     username,
			Email:        username + exampleEmailDomain,
			PasswordHash: "$2a$12$conformancefakehash",
			Status:       domain.StatusActive,
			Source:       domain.UserSourceIdentity,
			AuthzEpoch:   1,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		},
	)
	if reconErr != nil {
		t.Fatalf("conformCrossTenantWriteProbes: ReconstituteUser: %v", reconErr)
	}
	if err := repo.UpdateLockoutFields(ctx, testTenantIDOther, crossTenantUser); !isErrAuthUserNotFound(err) {
		t.Fatalf("UpdateLockoutFields cross-tenant: want ErrAuthUserNotFound, got %v", err)
	}
}

// conformConcurrentNoDeadlock: 50 goroutines making mixed reads/writes within a
// 30-second context deadline. The test fails if:
//   - any goroutine is still running waitBudget after the context deadline
//     fires (real deadlock — wg.Wait() would otherwise block until the
//     `go test -timeout` global timeout, hiding the bug);
//   - any goroutine returns an error other than context.DeadlineExceeded /
//     context.Canceled / KindConflict (the latter two are expected under
//     contention; an impl that returns "always succeeds" with deadline ignored
//     would also surface here when the deadline expires on a real op).
func conformConcurrentNoDeadlock(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "concurrent_"+uuid.NewString())

	const goroutines = 50
	ctx, cancel := context.WithTimeout(context.Background(), concurrencyDeadlineBudget)
	t.Cleanup(cancel)

	type goroutineResult struct {
		idx int
		err error
	}
	results := make(chan goroutineResult, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			var err error
			switch idx % 3 {
			case 0:
				_, err = repo.GetByID(ctx, u.ID)
			case 1:
				// Intentionally stale version — conflict is expected (and tolerated below).
				_, err = repo.UpdatePassword(ctx, testTenantID, u.ID, "$2a$12$concurrent", false, 0)
			case 2:
				err = txRunner.RunInTx(ctx, func(txCtx context.Context) error {
					_, e := repo.BumpAuthzEpoch(txCtx, testTenantID, u.ID, credentialfence.Mint())
					return e
				})
			}
			results <- goroutineResult{idx: idx, err: err}
		}(i)
	}

	// Bound wg.Wait() with a deadlock watchdog: deadline budget + small grace
	// for in-flight goroutines to observe ctx.Done and unwind.
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		// All goroutines returned within the budget; proceed to error inspection.
	case <-time.After(concurrencyDeadlineBudget + deadlockGracePeriod):
		t.Fatalf("Concurrent_NoDeadlock: goroutines did not finish within %v after deadline; "+
			"likely deadlock (impl ignored ctx.Done)",
			concurrencyDeadlineBudget+deadlockGracePeriod)
	}
	close(results)

	// Inspect goroutine errors. Tolerate context cancellation (contention under
	// deadline) and KindConflict (CAS losers); any other error signals an impl
	// bug.
	for r := range results {
		if r.err == nil {
			continue
		}
		if errors.Is(r.err, context.DeadlineExceeded) || errors.Is(r.err, context.Canceled) {
			continue
		}
		if isConflictErr(r.err) {
			continue
		}
		t.Errorf("Concurrent_NoDeadlock: goroutine %d returned unexpected error: %v", r.idx, r.err)
	}
}

// conformUpdateLockoutFieldsSucceeds (F22): writes count + lastFailedAt +
// lockedUntil into an existing user, then reads it back via GetByID and asserts
// the three lockout columns are persisted. Status / epoch / password columns
// must NOT be affected.
func conformUpdateLockoutFieldsSucceeds(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "lockfld_"+uuid.NewString())
	initialStatus := u.Status()
	initialEpoch := u.AuthzEpoch()

	// Build updated lockout state in memory.
	now := time.Now().UTC().Truncate(time.Millisecond)
	failedAt := now.Add(lockoutFixtureFailedGap)
	lockedUntil := now.Add(lockoutFixtureLockedUntil)
	updated, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:               u.ID,
		Username:         u.Username,
		Email:            u.Email,
		PasswordHash:     u.PasswordHash,
		PasswordVersion:  u.PasswordVersion,
		Status:           u.Status(),
		Source:           u.CreationSource,
		AuthzEpoch:       u.AuthzEpoch(),
		CreatedAt:        u.CreatedAt,
		UpdatedAt:        u.UpdatedAt,
		FailedLoginCount: 5,
		LastFailedAt:     &failedAt,
		LockedUntil:      &lockedUntil,
	})
	if err != nil {
		t.Fatalf("UpdateLockoutFields_Succeeds: ReconstituteUser: %v", err)
	}

	if err := repo.UpdateLockoutFields(context.Background(), testTenantID, updated); err != nil {
		t.Fatalf("UpdateLockoutFields_Succeeds: UpdateLockoutFields: %v", err)
	}

	got, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdateLockoutFields_Succeeds: GetByID: %v", err)
	}
	if got.FailedLoginCount() != 5 {
		t.Errorf("UpdateLockoutFields_Succeeds: failed_login_count: got %d, want 5", got.FailedLoginCount())
	}
	if got.LastFailedAt() == nil {
		t.Fatal("UpdateLockoutFields_Succeeds: last_failed_at must not be nil after UpdateLockoutFields")
	}
	if !got.LastFailedAt().Truncate(time.Millisecond).Equal(failedAt) {
		t.Errorf("UpdateLockoutFields_Succeeds: last_failed_at: got %v, want %v", got.LastFailedAt(), failedAt)
	}
	if got.AutoLockoutDeadline() == nil {
		t.Fatal("UpdateLockoutFields_Succeeds: locked_until must not be nil after UpdateLockoutFields")
	}
	if !got.AutoLockoutDeadline().Truncate(time.Millisecond).Equal(lockedUntil) {
		t.Errorf("UpdateLockoutFields_Succeeds: locked_until: got %v, want %v", got.AutoLockoutDeadline(), lockedUntil)
	}
	// Status and epoch must NOT be touched by UpdateLockoutFields.
	if got.Status() != initialStatus {
		t.Errorf("UpdateLockoutFields_Succeeds: status must not change: got %v, want %v", got.Status(), initialStatus)
	}
	if got.AuthzEpoch() != initialEpoch {
		t.Errorf("UpdateLockoutFields_Succeeds: authz_epoch must not change: got %d, want %d", got.AuthzEpoch(), initialEpoch)
	}
}

// conformUpdateLockoutFieldsNotFound (F22): UpdateLockoutFields on a
// non-existent userID must return ErrAuthUserNotFound. Returns the repo error
// so the caller asserts via the typed funnel at the test site (required by
// POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01: archtest does not
// follow cross-function helpers).
func conformUpdateLockoutFieldsNotFound(t *testing.T, factory UserRepoFactory) error {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	phantom := uuid.NewString()
	phantom2 := uuid.NewString() + exampleEmailDomain
	now := time.Now().UTC()
	// fakeHash is a syntactically valid bcrypt string used only as a test
	// placeholder; it is not a real credential.
	const fakeHash = "$2a$12$conformancefakehash"
	ghost, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:           phantom,
		Username:     "ghost_" + phantom,
		Email:        phantom2,
		PasswordHash: fakeHash,
		Status:       domain.StatusActive,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("UpdateLockoutFields_NotFound: ReconstituteUser: %v", err)
	}

	err = repo.UpdateLockoutFields(context.Background(), testTenantID, ghost)
	if err == nil {
		t.Fatal("UpdateLockoutFields_NotFound: must return error for non-existent user, got nil")
	}
	return err
}

// conformGetByIDForUpdateLockContention verifies the lock-hold guarantee shared
// by both implementations: a contender calling GetByIDForUpdate inside a
// RunInTx must block until the holder's RunInTx completes. The guarantee is
// delivered by different mechanisms (mem: store.mu held for the entire tx;
// PG: row-level lock held until commit/rollback), but the observable behavior
// — contender blocks while holder holds, then succeeds after release — is
// identical.
func conformGetByIDForUpdateLockContention(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "lockhold_"+uuid.NewString())

	holderEntered := make(chan struct{})
	releaseHolder := make(chan struct{})
	var wg sync.WaitGroup

	// Holder: enter tx, GetByIDForUpdate, signal entered, wait for explicit release.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), holderTimeout)
		defer cancel()
		_ = txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.GetByIDForUpdate(txCtx, testTenantID, u.ID)
			if err != nil {
				t.Errorf("holder GetByIDForUpdate: %v", err)
				return err
			}
			close(holderEntered)
			<-releaseHolder
			return nil
		})
	}()

	<-holderEntered

	// Contender: must block until releaseHolder is closed.
	contenderDone := make(chan struct{})
	contenderStart := time.Now()
	var contenderErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), holderTimeout)
		defer cancel()
		contenderErr = txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.GetByIDForUpdate(txCtx, testTenantID, u.ID)
			return err
		})
		close(contenderDone)
	}()

	// Within quickGracePeriod the contender must still be blocked.
	select {
	case <-contenderDone:
		t.Errorf(
			"GetByIDForUpdate_LockContention: contender returned in %v without holder release; "+
				"expected to block on lock", time.Since(contenderStart),
		)
	case <-time.After(quickGracePeriod):
		// Expected: contender is still blocked waiting for holder.
	}

	// Release holder; contender must now complete.
	close(releaseHolder)
	select {
	case <-contenderDone:
		// OK: contender unblocked after holder released.
	case <-time.After(holderTimeout):
		t.Fatal("GetByIDForUpdate_LockContention: contender did not complete within holderTimeout after holder release")
	}

	wg.Wait()
	if contenderErr != nil {
		t.Errorf("GetByIDForUpdate_LockContention: contender RunInTx err = %v, want nil", contenderErr)
	}
}

// ─── Narrow write methods (issue #828) ────────────────────────────────────────

// conformUpdateProfileSucceeds verifies UpdateProfile writes username + email +
// updated_at and returns the reconstituted aggregate, without touching status /
// password / lockout / epoch columns.
func conformUpdateProfileSucceeds(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "prof_"+uuid.NewString())
	initialStatus := u.Status()
	initialEpoch := u.AuthzEpoch()
	initialPwdHash := u.PasswordHash
	initialPwdVer := u.PasswordVersion

	newName := "renamed_" + uuid.NewString()
	newEmail := newName + exampleEmailDomain
	now := time.Now().UTC().Truncate(time.Millisecond)

	updated, err := repo.UpdateProfile(context.Background(), testTenantID, u.ID, nePtr(newName), nePtr(newEmail), now)
	if err != nil {
		t.Fatalf("UpdateProfile_Succeeds: UpdateProfile: %v", err)
	}
	if updated == nil {
		t.Fatal("UpdateProfile_Succeeds: returned user is nil")
	}
	if updated.Username != newName {
		t.Errorf("UpdateProfile_Succeeds: returned username: got %q, want %q", updated.Username, newName)
	}
	if updated.Email != newEmail {
		t.Errorf("UpdateProfile_Succeeds: returned email: got %q, want %q", updated.Email, newEmail)
	}

	// Re-read and verify the same columns persisted and untouched columns held.
	got, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdateProfile_Succeeds: GetByID: %v", err)
	}
	if got.Username != newName {
		t.Errorf("UpdateProfile_Succeeds: persisted username: got %q, want %q", got.Username, newName)
	}
	if got.Email != newEmail {
		t.Errorf("UpdateProfile_Succeeds: persisted email: got %q, want %q", got.Email, newEmail)
	}
	if got.Status() != initialStatus {
		t.Errorf("UpdateProfile_Succeeds: status must not change: got %v, want %v", got.Status(), initialStatus)
	}
	if got.AuthzEpoch() != initialEpoch {
		t.Errorf("UpdateProfile_Succeeds: authz_epoch must not change: got %d, want %d", got.AuthzEpoch(), initialEpoch)
	}
	if got.PasswordHash != initialPwdHash {
		t.Errorf("UpdateProfile_Succeeds: password_hash must not change")
	}
	if got.PasswordVersion != initialPwdVer {
		t.Errorf("UpdateProfile_Succeeds: password_version must not change")
	}
	if got.PasswordResetRequired() {
		t.Error("UpdateProfile_Succeeds: password_reset_required must remain false")
	}
}

// conformUpdateProfilePartialPATCH verifies nil-pointer arguments leave the
// corresponding column untouched (COALESCE semantics).
func conformUpdateProfilePartialPATCH(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "ppatch_"+uuid.NewString())
	originalName := u.Username
	originalEmail := u.Email

	// Update name only; email pointer is nil.
	newName := "only_name_changed_" + uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := repo.UpdateProfile(context.Background(), testTenantID, u.ID, nePtr(newName), nil, now); err != nil {
		t.Fatalf("UpdateProfile_PartialPATCH: name-only: %v", err)
	}
	got, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdateProfile_PartialPATCH: GetByID after name-only: %v", err)
	}
	if got.Username != newName {
		t.Errorf("UpdateProfile_PartialPATCH: name should change: got %q, want %q", got.Username, newName)
	}
	if got.Email != originalEmail {
		t.Errorf("UpdateProfile_PartialPATCH: email should NOT change with nil email pointer: got %q, want %q",
			got.Email, originalEmail)
	}

	// Update email only; name pointer is nil.
	newEmail := "only_email_" + uuid.NewString() + exampleEmailDomain
	if _, err := repo.UpdateProfile(context.Background(), testTenantID, u.ID, nil, nePtr(newEmail), now); err != nil {
		t.Fatalf("UpdateProfile_PartialPATCH: email-only: %v", err)
	}
	got, err = repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdateProfile_PartialPATCH: GetByID after email-only: %v", err)
	}
	if got.Email != newEmail {
		t.Errorf("UpdateProfile_PartialPATCH: email should change: got %q, want %q", got.Email, newEmail)
	}
	if got.Username != newName {
		t.Errorf("UpdateProfile_PartialPATCH: name should NOT change with nil name pointer: got %q, want %q",
			got.Username, newName)
	}

	_ = originalName // silence linter (kept for debug context)

	// Both nil: no-op. Username and email must stay at current values.
	currentName := newName // last set value
	currentEmail := newEmail
	if _, err := repo.UpdateProfile(context.Background(), testTenantID, u.ID, nil, nil, now); err != nil {
		t.Fatalf("UpdateProfile_PartialPATCH: nil+nil must not error: %v", err)
	}
	got, err = repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdateProfile_PartialPATCH: GetByID after nil+nil: %v", err)
	}
	if got.Username != currentName {
		t.Errorf("UpdateProfile_PartialPATCH: nil+nil must not change username: got %q, want %q",
			got.Username, currentName)
	}
	if got.Email != currentEmail {
		t.Errorf("UpdateProfile_PartialPATCH: nil+nil must not change email: got %q, want %q",
			got.Email, currentEmail)
	}
}

// conformUpdateProfileNotFound verifies missing userID returns ErrAuthUserNotFound.
func conformUpdateProfileNotFound(t *testing.T, factory UserRepoFactory) error {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	phantom := uuid.NewString()
	ghostName := "ghost_" + phantom
	ghostEmail := ghostName + exampleEmailDomain
	_, err := repo.UpdateProfile(context.Background(), testTenantID, phantom, nePtr(ghostName), nePtr(ghostEmail),
		time.Now().UTC().Truncate(time.Millisecond))
	if err == nil {
		t.Fatal("UpdateProfile_NotFound: must return error for non-existent user, got nil")
	}
	return err
}

// conformUpdateProfileDuplicateUsername verifies the port contract: renaming
// user A to user B's username must surface ErrAuthUserDuplicate. PG enforces
// via UNIQUE(username); mem mirrors via byName lookup. Pins mem/PG parity.
func conformUpdateProfileDuplicateUsername(t *testing.T, factory UserRepoFactory) error {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	a := seedActive(t, txRunner, repo, uuid.NewString(), "dup_a_"+uuid.NewString())
	b := seedActive(t, txRunner, repo, uuid.NewString(), "dup_b_"+uuid.NewString())

	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err := repo.UpdateProfile(context.Background(), testTenantID, a.ID, nePtr(b.Username), nil, now)
	if err == nil {
		t.Fatal("UpdateProfile_DuplicateUsername: rename to existing username must error, got nil")
	}
	return err
}

// conformUpdateProfileDuplicateEmail verifies the port contract for the email
// uniqueness mirror.
func conformUpdateProfileDuplicateEmail(t *testing.T, factory UserRepoFactory) error {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	a := seedActive(t, txRunner, repo, uuid.NewString(), "dupe_a_"+uuid.NewString())
	b := seedActive(t, txRunner, repo, uuid.NewString(), "dupe_b_"+uuid.NewString())

	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err := repo.UpdateProfile(context.Background(), testTenantID, a.ID, nil, nePtr(b.Email), now)
	if err == nil {
		t.Fatal("UpdateProfile_DuplicateEmail: change to existing email must error, got nil")
	}
	return err
}

// conformUpdateProfileSameValuesNoOp verifies the self-match edge case:
// PATCH with the user's current username + email must succeed (not 409). The
// uniqueness check's collider ID must equal the target userID to skip the
// duplicate error — closes the "I am my own collider" branch.
func conformUpdateProfileSameValuesNoOp(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "same_"+uuid.NewString())
	originalName := u.Username
	originalEmail := u.Email

	now := time.Now().UTC().Truncate(time.Millisecond)
	updated, err := repo.UpdateProfile(context.Background(), testTenantID, u.ID, nePtr(originalName), nePtr(originalEmail), now)
	if err != nil {
		t.Fatalf("UpdateProfile_SameValuesNoOp: same-values PATCH must succeed: %v", err)
	}
	if updated.Username != originalName {
		t.Errorf("UpdateProfile_SameValuesNoOp: username unchanged: got %q, want %q", updated.Username, originalName)
	}
	if updated.Email != originalEmail {
		t.Errorf("UpdateProfile_SameValuesNoOp: email unchanged: got %q, want %q", updated.Email, originalEmail)
	}
}

// conformUpdateLockStateSucceeds verifies UpdateLockState persists status +
// updated_at while leaving username / email / password / epoch untouched.
// Status==Locked path: lockout columns are NOT auto-zeroed.
func conformUpdateLockStateSucceeds(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "lock_"+uuid.NewString())
	initialName := u.Username
	initialEmail := u.Email
	initialPwdHash := u.PasswordHash
	initialEpoch := u.AuthzEpoch()

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := repo.UpdateLockState(context.Background(), testTenantID, u.ID, domain.StatusLocked, now); err != nil {
		t.Fatalf("UpdateLockState_Succeeds: UpdateLockState: %v", err)
	}

	got, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdateLockState_Succeeds: GetByID: %v", err)
	}
	if got.Status() != domain.StatusLocked {
		t.Errorf("UpdateLockState_Succeeds: status: got %v, want Locked", got.Status())
	}
	if got.Username != initialName {
		t.Errorf("UpdateLockState_Succeeds: username must not change: got %q, want %q", got.Username, initialName)
	}
	if got.Email != initialEmail {
		t.Errorf("UpdateLockState_Succeeds: email must not change: got %q, want %q", got.Email, initialEmail)
	}
	if got.PasswordHash != initialPwdHash {
		t.Errorf("UpdateLockState_Succeeds: password_hash must not change")
	}
	if got.AuthzEpoch() != initialEpoch {
		t.Errorf("UpdateLockState_Succeeds: authz_epoch must not change: got %d, want %d", got.AuthzEpoch(), initialEpoch)
	}
}

// conformUpdateLockStateActivateClearsLockout verifies the column-level
// invariant: UpdateLockState(status=Active) atomically zeros
// failed_login_count / last_failed_at / locked_until in the same statement,
// closing the PR #585 P1#3 admin-unlock re-lock race at the schema layer.
func conformUpdateLockStateActivateClearsLockout(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "act_"+uuid.NewString())

	// Seed lockout state via UpdateLockoutFields (the auto-lockout path).
	now := time.Now().UTC().Truncate(time.Millisecond)
	failedAt := now.Add(lockoutFixtureFailedGap)
	lockedUntil := now.Add(lockoutFixtureLockedUntil)
	withLockout, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:               u.ID,
		Username:         u.Username,
		Email:            u.Email,
		PasswordHash:     u.PasswordHash,
		PasswordVersion:  u.PasswordVersion,
		Status:           domain.StatusLocked,
		Source:           u.CreationSource,
		AuthzEpoch:       u.AuthzEpoch(),
		CreatedAt:        u.CreatedAt,
		UpdatedAt:        now,
		FailedLoginCount: 5,
		LastFailedAt:     &failedAt,
		LockedUntil:      &lockedUntil,
	})
	if err != nil {
		t.Fatalf("UpdateLockState_ActivateClearsLockout: ReconstituteUser: %v", err)
	}
	if err := repo.UpdateLockoutFields(context.Background(), testTenantID, withLockout); err != nil {
		t.Fatalf("UpdateLockState_ActivateClearsLockout: seed UpdateLockoutFields: %v", err)
	}

	// Move the user to StatusLocked (no auto-zero on Lock).
	if err := repo.UpdateLockState(context.Background(), testTenantID, u.ID, domain.StatusLocked, now); err != nil {
		t.Fatalf("UpdateLockState_ActivateClearsLockout: UpdateLockState(Locked): %v", err)
	}
	mid, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdateLockState_ActivateClearsLockout: GetByID after Lock: %v", err)
	}
	if mid.FailedLoginCount() != 5 {
		t.Errorf("UpdateLockState_ActivateClearsLockout: failed_login_count must persist on Lock: got %d, want 5",
			mid.FailedLoginCount())
	}

	// Now Activate — column-level invariant zeros the three lockout columns.
	now2 := now.Add(time.Second)
	if err := repo.UpdateLockState(context.Background(), testTenantID, u.ID, domain.StatusActive, now2); err != nil {
		t.Fatalf("UpdateLockState_ActivateClearsLockout: UpdateLockState(Active): %v", err)
	}
	got, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdateLockState_ActivateClearsLockout: GetByID after Activate: %v", err)
	}
	if got.Status() != domain.StatusActive {
		t.Errorf("UpdateLockState_ActivateClearsLockout: status: got %v, want Active", got.Status())
	}
	if got.FailedLoginCount() != 0 {
		t.Errorf("UpdateLockState_ActivateClearsLockout: failed_login_count must be 0 after Activate: got %d",
			got.FailedLoginCount())
	}
	if got.LastFailedAt() != nil {
		t.Errorf("UpdateLockState_ActivateClearsLockout: last_failed_at must be nil after Activate: got %v",
			got.LastFailedAt())
	}
	if got.AutoLockoutDeadline() != nil {
		t.Errorf("UpdateLockState_ActivateClearsLockout: locked_until must be nil after Activate: got %v",
			got.AutoLockoutDeadline())
	}
}

// conformUpdatePasswordResetFlagSucceeds verifies the flag is persisted without
// touching password_hash / password_version / status / lockout / epoch.
func conformUpdatePasswordResetFlagSucceeds(t *testing.T, factory UserRepoFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	u := seedActive(t, txRunner, repo, uuid.NewString(), "prf_"+uuid.NewString())
	initialPwdHash := u.PasswordHash
	initialPwdVer := u.PasswordVersion
	initialStatus := u.Status()
	initialEpoch := u.AuthzEpoch()

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := repo.UpdatePasswordResetFlag(context.Background(), testTenantID, u.ID, true, now); err != nil {
		t.Fatalf("UpdatePasswordResetFlag_Succeeds: set true: %v", err)
	}
	got, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdatePasswordResetFlag_Succeeds: GetByID after true: %v", err)
	}
	if !got.PasswordResetRequired() {
		t.Error("UpdatePasswordResetFlag_Succeeds: password_reset_required must be true")
	}
	if got.PasswordHash != initialPwdHash {
		t.Error("UpdatePasswordResetFlag_Succeeds: password_hash must not change")
	}
	if got.PasswordVersion != initialPwdVer {
		t.Error("UpdatePasswordResetFlag_Succeeds: password_version must not change")
	}
	if got.Status() != initialStatus {
		t.Errorf("UpdatePasswordResetFlag_Succeeds: status must not change: got %v, want %v", got.Status(), initialStatus)
	}
	if got.AuthzEpoch() != initialEpoch {
		t.Errorf("UpdatePasswordResetFlag_Succeeds: authz_epoch must not change: got %d, want %d",
			got.AuthzEpoch(), initialEpoch)
	}

	// Toggle off — same isolation.
	now2 := now.Add(time.Second)
	if err := repo.UpdatePasswordResetFlag(context.Background(), testTenantID, u.ID, false, now2); err != nil {
		t.Fatalf("UpdatePasswordResetFlag_Succeeds: set false: %v", err)
	}
	got, err = repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("UpdatePasswordResetFlag_Succeeds: GetByID after false: %v", err)
	}
	if got.PasswordResetRequired() {
		t.Error("UpdatePasswordResetFlag_Succeeds: password_reset_required must be false")
	}
}

// conformUpdateLockStateNotFound (B1): UpdateLockState on a non-existent userID
// must return ErrAuthUserNotFound. Returns the repo error so the caller asserts
// via the typed funnel at the test site (required by
// POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01: archtest does not
// follow cross-function helpers).
func conformUpdateLockStateNotFound(t *testing.T, factory UserRepoFactory) error {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	err := repo.UpdateLockState(context.Background(), testTenantID, uuid.NewString(), domain.StatusLocked, time.Now().UTC())
	if err == nil {
		t.Fatal("UpdateLockState_NotFound: must return error for non-existent user, got nil")
	}
	return err
}

// conformUpdatePasswordResetFlagNotFound (B2): UpdatePasswordResetFlag on a
// non-existent userID must return ErrAuthUserNotFound. Returns the repo error
// so the caller asserts via the typed funnel at the test site (required by
// POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01: archtest does not
// follow cross-function helpers).
func conformUpdatePasswordResetFlagNotFound(t *testing.T, factory UserRepoFactory) error {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	err := repo.UpdatePasswordResetFlag(context.Background(), testTenantID, uuid.NewString(), true, time.Now().UTC())
	if err == nil {
		t.Fatal("UpdatePasswordResetFlag_NotFound: must return error for non-existent user, got nil")
	}
	return err
}

// ─── RoleRepository conformance ───────────────────────────────────────────────

// RoleRepoFactory constructs a fresh ports.RoleRepository, a matching
// ports.UserRepository (needed to seed users across tenants), a shared
// persistence.TxRunner, and a cleanup func. Called once per sub-test.
type RoleRepoFactory func(t *testing.T) (
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	txRunner persistence.TxRunner,
	cleanup func(),
)

// RunRoleRepoConformance executes the RoleRepository contract acceptance suite.
// All implementations (mem, PG) must call this from a _test.go in their package.
//
// Tenancy (#1337 PR-2a, review F5): every RoleRepository method takes a mandatory
// tenant.TenantID, so the suite asserts cross-tenant isolation across the read
// (GetByID/GetByUserID), list (ListByUserID), count (CountByRole) and last-admin
// (CountEffectiveAdmins/EffectiveAdminExists) surfaces — not just the AssignToUser
// write path. The last-admin per-tenant assertion is the security-critical one: a
// tenant must never count another tenant's effective admins.
func RunRoleRepoConformance(t *testing.T, factory RoleRepoFactory) {
	t.Helper()
	t.Run("AssignToUser_CrossTenant_RejectsUser", func(t *testing.T) {
		conformRoleAssignCrossTenantUser(t, factory)
	})
	t.Run("Reads_CrossTenant_Invisible", func(t *testing.T) {
		conformRoleReadsCrossTenant(t, factory)
	})
	t.Run("EffectiveAdmin_CrossTenant_PerTenant", func(t *testing.T) {
		conformEffectiveAdminCrossTenant(t, factory)
	})
}

// seedRoleAssignment creates role roleID in tenant tid and assigns it to an
// active user (seeded in the same tenant), returning the user id. Shared by the
// RoleRepository cross-tenant read/count sub-tests.
func seedRoleAssignment(
	t *testing.T,
	roleRepo ports.RoleRepository, userRepo ports.UserRepository, txRunner persistence.TxRunner,
	tid tenant.TenantID, roleID string,
) string {
	t.Helper()
	user := seedActiveInTenant(t, txRunner, userRepo, tid, uuid.NewString(), "role_seed_"+uuid.NewString())
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		return roleRepo.Create(ctx, tid, &domain.Role{ID: roleID, Name: roleID})
	}); err != nil {
		t.Fatalf("seedRoleAssignment: create role %q in tenant %q: %v", roleID, tid, err)
	}
	if _, err := roleRepo.AssignToUser(context.Background(), tid, user.ID, roleID); err != nil {
		t.Fatalf("seedRoleAssignment: assign role %q to user in tenant %q: %v", roleID, tid, err)
	}
	return user.ID
}

// conformRoleReadsCrossTenant (F5): a role + assignment created in tenant A must
// be invisible to GetByID/GetByUserID/ListByUserID/CountByRole scoped to tenant
// B, while remaining visible from tenant A.
func conformRoleReadsCrossTenant(t *testing.T, factory RoleRepoFactory) {
	t.Helper()
	roleRepo, userRepo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	roleID := "role_reads_" + uuid.NewString()
	userID := seedRoleAssignment(t, roleRepo, userRepo, txRunner, testTenantID, roleID)
	ctx := context.Background()
	listParams := query.ListParams{Limit: 50, Sort: []query.SortColumn{
		{Name: "name", Direction: query.SortASC},
		{Name: "id", Direction: query.SortASC},
	}}

	// Tenant B (testTenantIDOther) must see nothing.
	if got, err := roleRepo.GetByUserID(ctx, testTenantIDOther, userID); err != nil || len(got) != 0 {
		t.Errorf("GetByUserID(tenantB): want 0 roles/no error, got %d roles err=%v", len(got), err)
	}
	if got, err := roleRepo.ListByUserID(ctx, testTenantIDOther, userID, listParams); err != nil || len(got) != 0 {
		t.Errorf("ListByUserID(tenantB): want 0 roles/no error, got %d roles err=%v", len(got), err)
	}
	if n, err := roleRepo.CountByRole(ctx, testTenantIDOther, roleID); err != nil || n != 0 {
		t.Errorf("CountByRole(tenantB): want 0/no error, got %d err=%v", n, err)
	}
	if r, err := roleRepo.GetByID(ctx, testTenantIDOther, roleID); err == nil && r != nil {
		t.Errorf("GetByID(tenantB): tenant-A role must not be visible, got %+v", r)
	}

	// Tenant A (testTenantID) still sees the role + assignment.
	if got, err := roleRepo.GetByUserID(ctx, testTenantID, userID); err != nil || len(got) != 1 {
		t.Fatalf("GetByUserID(tenantA): want 1 role/no error, got %d roles err=%v", len(got), err)
	}
	if n, err := roleRepo.CountByRole(ctx, testTenantID, roleID); err != nil || n != 1 {
		t.Errorf("CountByRole(tenantA): want 1/no error, got %d err=%v", n, err)
	}
	if got, err := roleRepo.ListByUserID(ctx, testTenantID, userID, listParams); err != nil || len(got) != 1 {
		t.Errorf("ListByUserID(tenantA): want 1 role/no error, got %d roles err=%v", len(got), err)
	}
}

// conformEffectiveAdminCrossTenant (F5): the last-admin invariant counters must
// be per-tenant. An effective admin seeded in tenant A must NOT be counted by
// CountEffectiveAdmins/EffectiveAdminExists scoped to tenant B — otherwise a
// tenant could be blocked from (or wrongly allowed) removing its last admin
// because another tenant happens to have one.
func conformEffectiveAdminCrossTenant(t *testing.T, factory RoleRepoFactory) {
	t.Helper()
	roleRepo, userRepo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	seedRoleAssignment(t, roleRepo, userRepo, txRunner, testTenantID, auth.RoleAdmin)
	ctx := context.Background()

	// Tenant A has exactly one effective admin.
	if n, err := roleRepo.CountEffectiveAdmins(ctx, testTenantID); err != nil || n != 1 {
		t.Errorf("CountEffectiveAdmins(tenantA): want 1/no error, got %d err=%v", n, err)
	}
	if ok, err := roleRepo.EffectiveAdminExists(ctx, testTenantID); err != nil || !ok {
		t.Errorf("EffectiveAdminExists(tenantA): want true/no error, got %v err=%v", ok, err)
	}

	// Tenant B must see zero — the tenant-A admin is invisible.
	if n, err := roleRepo.CountEffectiveAdmins(ctx, testTenantIDOther); err != nil || n != 0 {
		t.Errorf("CountEffectiveAdmins(tenantB): want 0/no error, got %d err=%v", n, err)
	}
	if ok, err := roleRepo.EffectiveAdminExists(ctx, testTenantIDOther); err != nil || ok {
		t.Errorf("EffectiveAdminExists(tenantB): want false/no error, got %v err=%v", ok, err)
	}
}

// conformRoleAssignCrossTenantUser (F4): assigning a role (which exists in
// testTenantID) to a user that lives in testTenantIDOther must return
// ErrAuthUserNotFound — the same error as "user not found", so the caller
// cannot enumerate cross-tenant user existence.
func conformRoleAssignCrossTenantUser(t *testing.T, factory RoleRepoFactory) {
	t.Helper()
	roleRepo, userRepo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	// Seed a role in tenant A (testTenantID).
	roleID := "role_xten_" + uuid.NewString()
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		return roleRepo.Create(ctx, testTenantID, &domain.Role{ID: roleID, Name: roleID})
	}); err != nil {
		t.Fatalf("conformRoleAssignCrossTenantUser: seed role: %v", err)
	}

	// Seed a user in tenant B (testTenantIDOther).
	userB := seedActiveInTenant(t, txRunner, userRepo, testTenantIDOther, uuid.NewString(), "xten_user_"+uuid.NewString())

	// Attempt to assign the tenant-A role to the tenant-B user (cross-tenant write).
	_, err := roleRepo.AssignToUser(context.Background(), testTenantID, userB.ID, roleID)
	if err == nil {
		t.Fatal("AssignToUser_CrossTenant: must return error for user from different tenant, got nil")
	}
	if !isErrAuthUserNotFound(err) {
		t.Errorf("AssignToUser_CrossTenant: want ErrAuthUserNotFound, got %v", err)
	}

	// Sanity: assigning to a user in the SAME tenant must still succeed.
	userA := seedActiveInTenant(t, txRunner, userRepo, testTenantID, uuid.NewString(), "xten_usera_"+uuid.NewString())
	changed, err := roleRepo.AssignToUser(context.Background(), testTenantID, userA.ID, roleID)
	if err != nil {
		t.Fatalf("AssignToUser_CrossTenant: same-tenant assign must succeed, got %v", err)
	}
	if !changed {
		t.Error("AssignToUser_CrossTenant: same-tenant first assign must return changed=true")
	}
}
