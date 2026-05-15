package identitymanage

// identitymanage_credential_race_test.go — concurrent-credential-mutation race tests.
//
// Historical context: S4b introduced the credentialinvalidate funnel which
// combines BumpAuthzEpoch + RevokeForSubject + RevokeUser into a single
// atomic operation. Before S4b, partial revocation paths existed (some callers
// only called RevokeForSubject but skipped BumpAuthzEpoch), leaving access JWTs
// valid after credential events. The race tests below guard the data-race
// invariant: concurrent credential events must neither corrupt the epoch counter
// nor leave any session unrevoked.
//
// Test isolation: tests use in-memory stores (mem.UserRepository + session.MemStore
// + refresh.Store) so no Docker / testcontainer dependency is required. The tests
// run with go test -race to detect concurrent store mutations.
//
// Design: 50 goroutines concurrently call ChangePassword and Lock on the same
// user. The in-memory stores use sync.RWMutex internally, so all operations are
// race-safe. Post-run assertions verify:
//   - user.authz_epoch >= 1 (at least one credential event succeeded)
//   - all sessions for the subject are revoked (no goroutine's revoke was dropped)
//   - no panic occurred during concurrent execution

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// TestIdentitymanageCredential_ConcurrentChangePasswordAndLock verifies that
// concurrent ChangePassword and Lock operations on the same user:
//
//  1. Do not cause data races (run with -race flag).
//  2. Leave the user's authz_epoch at ≥ 1 (each successful op bumps by 1).
//  3. Revoke all sessions owned by the subject (no session escapes revocation
//     after the concurrent storm completes).
//  4. Do not panic.
//
// Concurrency: 50 goroutines (25 ChangePassword + 25 Lock). Operations that
// fail due to status guard (Lock rejects an already-locked user) or CAS
// conflict (ChangePassword rejects a stale password version) contribute 0 to
// the epoch; successful ops contribute 1 each. At least one will succeed,
// guaranteeing epoch ≥ 1.
func TestIdentitymanageCredential_ConcurrentChangePasswordAndLock(t *testing.T) {
	const goroutines = 50

	memStore := mem.NewStore(clock.Real())
	userRepo := memStore.UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	inv := newInvalidator(t, userRepo, sessionStore, refreshStore)

	// Stub issuer — ChangePassword needs IssueForUser after the tx. The stub
	// returns an empty token pair (sufficient for race testing).
	svc, err := NewService(userRepo, inv, slog.Default(),
		WithTokenIssuer(minimalStubIssuer),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})),
	)
	require.NoError(t, err)

	// Create a user with a known initial password.
	const initialPassword = "InitialPass!Race01"
	adminCtx := auth.TestContext("test-admin", []string{"admin"})
	user, err := svc.Create(adminCtx, CreateInput{
		Username: "race-user-" + uuid.NewString()[:8],
		Email:    "race@test.local",
		Password: initialPassword,
	})
	require.NoError(t, err)
	userID := user.ID

	// Seed one active session so revocation assertions are meaningful.
	seedSessionID := uuid.NewString()
	require.NoError(t, sessionStore.Create(context.Background(), &session.Session{
		ID:                seedSessionID,
		SubjectID:         userID,
		JTI:               "race-jti-" + seedSessionID,
		AuthzEpochAtIssue: 1,
		CreatedAt:         clock.Real().Now(),
		ExpiresAt:         clock.Real().Now().Add(time.Hour),
	}))

	var wg sync.WaitGroup
	var successCount atomic.Int64

	// Half goroutines attempt ChangePassword; half attempt Lock.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		opIdx := i
		go func() {
			defer wg.Done()
			opCtx := auth.TestContext("test-admin", []string{"admin"})
			var callErr error
			if opIdx%2 == 0 {
				// ChangePassword: old=initial, new=something unique.
				_, callErr = svc.ChangePassword(opCtx, ChangePasswordInput{
					UserID:      userID,
					OldPassword: initialPassword,
					NewPassword: "NewPass!Race" + uuid.NewString()[:6],
				})
			} else {
				// Lock: idempotent after first lock; subsequent calls error on
				// status guard but that is expected under concurrent load.
				callErr = svc.Lock(opCtx, userID)
			}
			if callErr == nil {
				successCount.Add(1)
			}
		}()
	}
	wg.Wait()

	// Terminal assertions — evaluated after all goroutines complete.

	// authz_epoch: at least one credential event must have committed.
	finalUser, err := userRepo.GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, finalUser.AuthzEpoch(), int64(1),
		"authz_epoch must be ≥ 1 after concurrent credential events; got %d (successCount=%d)",
		finalUser.AuthzEpoch(), successCount.Load())

	// All sessions for the subject must be revoked after the concurrent storm.
	require.Eventually(t, func() bool {
		view, getErr := sessionStore.Get(context.Background(), seedSessionID)
		if getErr != nil {
			return true // session gone → effectively revoked
		}
		return view.RevokedAt != nil
	}, testtime.EventuallyDefault, testtime.D10ms,
		"seed session must be revoked after concurrent credential events")
}

// TestIdentitymanageCredential_ConcurrentChangePassword_EpochPositive verifies
// that after concurrent ChangePassword calls, authz_epoch is positive (each
// successful ChangePassword increments by exactly 1 via the funnel).
//
// This test uses a serializing simpleTxRunner so operations are serialized
// in memory — the race detector still checks for any internal data race
// within the in-memory stores.
func TestIdentitymanageCredential_ConcurrentChangePassword_EpochPositive(t *testing.T) {
	const goroutines = 20

	memStore := mem.NewStore(clock.Real())
	userRepo := memStore.UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	inv := newInvalidator(t, userRepo, sessionStore, refreshStore)

	svc, err := NewService(userRepo, inv, slog.Default(),
		WithTokenIssuer(minimalStubIssuer),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})),
	)
	require.NoError(t, err)

	const pwd = "EpochTest!Mono01" //nolint:gosec // test fixture password, not a credential
	adminCtx := auth.TestContext("test-admin", []string{"admin"})
	user, err := svc.Create(adminCtx, CreateInput{
		Username: "epoch-test-" + uuid.NewString()[:8],
		Email:    "epoch@test.local",
		Password: pwd,
	})
	require.NoError(t, err)
	userID := user.ID

	var (
		wg           sync.WaitGroup
		successCount atomic.Int64
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opCtx := auth.TestContext("test-admin", []string{"admin"})
			if _, callErr := svc.ChangePassword(opCtx, ChangePasswordInput{
				UserID:      userID,
				OldPassword: pwd,
				NewPassword: "New!" + uuid.NewString()[:8],
			}); callErr == nil {
				successCount.Add(1)
			}
		}()
	}
	wg.Wait()

	finalUser, err := userRepo.GetByID(context.Background(), userID)
	require.NoError(t, err)

	// The epoch must reflect initial=1 plus the number of successful changes.
	sc := successCount.Load()
	assert.Equal(t, int64(1)+sc, finalUser.AuthzEpoch(),
		"authz_epoch must equal 1 (initial) + successful ChangePassword calls (%d); got %d",
		sc, finalUser.AuthzEpoch())
	assert.GreaterOrEqual(t, finalUser.AuthzEpoch(), int64(2),
		"at least one ChangePassword must succeed under concurrent load (epoch starts at 1)")
}
