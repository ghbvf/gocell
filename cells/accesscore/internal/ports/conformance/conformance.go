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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	t.Run("BumpAuthzEpoch_Succeeds", func(t *testing.T) {
		conformBumpAuthzEpochSucceeds(t, factory)
	})
	t.Run("BumpAuthzEpoch_MonotonicIncrement", func(t *testing.T) {
		conformBumpAuthzEpochMonotonic(t, factory)
	})
	t.Run("GetByIDForUpdate_LockContention", func(t *testing.T) {
		conformGetByIDForUpdateLockContention(t, factory)
	})
	t.Run("NotFound_PropagatesErrAuthUserNotFound", func(t *testing.T) {
		conformNotFoundPropagates(t, factory)
	})
	t.Run("Concurrent_NoDeadlock", func(t *testing.T) {
		conformConcurrentNoDeadlock(t, factory)
	})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// seedActive creates and persists an active user inside a RunInTx call.
// It uses unique IDs so parallel sub-tests don't collide.
func seedActive(t *testing.T, txRunner persistence.TxRunner, repo ports.UserRepository, id, username string) *domain.User {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{ //nolint:gosec // test helper, not real credentials
		ID:           id,
		Username:     username,
		Email:        username + "@example.com",
		PasswordHash: "$2a$12$conformancefakehash",
		Status:       domain.StatusActive,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("seedActive: ReconstituteUser: %v", err)
	}
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		return repo.Create(ctx, u)
	}); err != nil {
		t.Fatalf("seedActive: Create: %v", err)
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

	_, err := repo.GetByIDForUpdate(context.Background(), u.ID)
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
		got, err = repo.GetByIDForUpdate(ctx, u.ID)
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

	_, err := repo.GetByUsernameForUpdate(context.Background(), u.Username)
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
		got, err = repo.GetByUsernameForUpdate(ctx, u.Username)
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
		context.Background(), u.ID, "$2a$12$newhash", false, initialVersion,
	)
	if err != nil {
		t.Fatalf("UpdatePassword_Succeeds: %v", err)
	}
	if newVersion != initialVersion+1 {
		t.Errorf("UpdatePassword_Succeeds: want version %d, got %d", initialVersion+1, newVersion)
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
		_, err1 := repo.UpdatePassword(context.Background(), u.ID, "$2a$12$hash1", false, 0)
		if err1 != nil {
			t.Fatalf("UpdatePassword_CASConflict/mem: first update failed: %v", err1)
		}

		_, err2 := repo.UpdatePassword(context.Background(), u.ID, "$2a$12$hash2", false, 0)
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
				context.Background(), u.ID,
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
		newEpoch, err = repo.BumpAuthzEpoch(ctx, u.ID)
		return err
	}); err != nil {
		t.Fatalf("BumpAuthzEpoch_Succeeds: %v", err)
	}
	if newEpoch != initialEpoch+1 {
		t.Errorf("BumpAuthzEpoch_Succeeds: want epoch %d, got %d", initialEpoch+1, newEpoch)
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
		epoch1, err = repo.BumpAuthzEpoch(ctx, u.ID)
		return err
	}); err != nil {
		t.Fatalf("BumpAuthzEpoch_MonotonicIncrement: first bump: %v", err)
	}

	var epoch2 int64
	if err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		epoch2, err = repo.BumpAuthzEpoch(ctx, u.ID)
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

	_, err = repo.GetByUsername(context.Background(), "nobody_"+phantom)
	if err == nil {
		t.Fatal("NotFound: GetByUsername on unknown username must return error, got nil")
	}
	if !isErrAuthUserNotFound(err) {
		t.Errorf("NotFound: GetByUsername must return ErrAuthUserNotFound, got %v", err)
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
				_, err = repo.UpdatePassword(ctx, u.ID, "$2a$12$concurrent", false, 0)
			case 2:
				err = txRunner.RunInTx(ctx, func(txCtx context.Context) error {
					_, e := repo.BumpAuthzEpoch(txCtx, u.ID)
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
			_, err := repo.GetByIDForUpdate(txCtx, u.ID)
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
			_, err := repo.GetByIDForUpdate(txCtx, u.ID)
			return err
		})
		close(contenderDone)
	}()

	// Within quickGracePeriod the contender must still be blocked.
	select {
	case <-contenderDone:
		t.Errorf(
			"GetByIDForUpdate_LockContention: contender returned in %v without holder release; "+
				"expected to block on lock", time.Since(contenderStart))
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
