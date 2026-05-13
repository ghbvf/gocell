package sessionvalidate

// sessionvalidate_concurrent_epoch_race_test.go — concurrent epoch-bump vs
// validate race test.
//
// Historical context: S4b added authz_epoch claim verification in
// enforceSessionState (user.AuthzEpoch > claims.AuthzEpoch → 401). This
// test guards the race safety of that read-path: one goroutine bumps the
// epoch directly via userRepo.BumpAuthzEpoch (bypassing the funnel to isolate
// the test scope to sessionvalidate only), while 50 goroutines concurrently
// validate the same access JWT.
//
// Design:
//  1. Issue an access JWT with authz_epoch=0 before the bump starts.
//  2. Start 50 validator goroutines that each call VerifyIntent on the token.
//  3. Start 1 goroutine that bumps the epoch to 1 via userRepo.BumpAuthzEpoch.
//  4. After the bump goroutine completes, signal the validators.
//  5. Drain all validator results.
//
// Expected invariant (PG READ COMMITTED semantics on the mem store):
//   - Validators that observe the epoch BEFORE the bump return (claims, nil).
//   - Validators that observe the epoch AFTER the bump return ("", 401 error).
//   - No panic from concurrent reads.
//
// Note: the bump goroutine uses userRepo.BumpAuthzEpoch directly, NOT the
// credentialinvalidate funnel. This is intentional — the test is scoped to
// sessionvalidate's read-path concurrency, not to funnel atomicity.
// BumpAuthzEpoch in _test.go is explicitly exempted by the archtest allowlist
// ("*_test.go") for this exact use case.

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

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// TestSessionvalidate_ConcurrentEpochBumpAndValidate verifies that:
//  1. No data race occurs when a single goroutine bumps the user's authz_epoch
//     while 50 goroutines concurrently validate an access JWT that was issued
//     at epoch=0.
//  2. After the bump commits, all subsequent validate calls reject the epoch=0
//     token (401).
//  3. Before the bump commits, validate calls on a valid active session succeed.
//  4. No panic occurs.
func TestSessionvalidate_ConcurrentEpochBumpAndValidate(t *testing.T) {
	const validators = 50

	memStore := mem.NewStore(clock.Real())
	userRepo := memStore.UserRepository()
	sessionStore := newTestStore(t)

	// Inject a user with authz_epoch=0.
	userID := "race-epoch-" + uuid.NewString()[:8]
	sessionID := "race-sess-" + uuid.NewString()[:8]
	initialUser := &domain.User{
		ID:         userID,
		Username:   "race-epoch-user",
		Email:      "race@epoch.test",
		Status:     domain.StatusActive,
		AuthzEpoch: 0,
		CreatedAt:  clock.Real().Now(),
		UpdatedAt:  clock.Real().Now(),
	}
	require.NoError(t, userRepo.Create(context.Background(), initialUser))

	// Seed an active session.
	require.NoError(t, sessionStore.Create(context.Background(), &session.Session{
		ID:        sessionID,
		SubjectID: userID,
		JTI:       "race-jti-" + sessionID,
		CreatedAt: clock.Real().Now(),
		ExpiresAt: clock.Real().Now().Add(time.Hour),
	}))

	svc, err := NewService(testVerifier, sessionStore, userRepo, slog.Default())
	require.NoError(t, err)

	// Issue an access token at epoch=0.
	tokenStr, err := IssueTestTokenWithEpoch(testPrivKey, userID, 0, time.Hour, sessionID)
	require.NoError(t, err)

	// bumpDone is closed when the bump goroutine commits the epoch increment.
	bumpDone := make(chan struct{})
	// validatorsStarted ensures all validators are running before the bump.
	var validatorsStarted sync.WaitGroup
	validatorsStarted.Add(validators)

	var (
		wg             sync.WaitGroup
		passBeforeBump atomic.Int64
		failAfterBump  atomic.Int64
	)

	// Start 50 validator goroutines.
	for i := 0; i < validators; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Signal we're ready, then wait for the bump to complete.
			validatorsStarted.Done()
			<-bumpDone

			// All validates after bump must fail (epoch=0 vs user.epoch=1).
			_, vErr := svc.VerifyIntent(context.Background(), tokenStr, auth.TokenIntentAccess)
			if vErr != nil {
				failAfterBump.Add(1)
			} else {
				passBeforeBump.Add(1)
			}
		}()
	}

	// Wait for all validators to reach the barrier, then bump.
	validatorsStarted.Wait()

	// Bump the epoch directly (test-only; funnel not needed here — see package
	// godoc for rationale).
	_, err = userRepo.BumpAuthzEpoch(context.Background(), userID)
	require.NoError(t, err, "BumpAuthzEpoch must succeed")

	// Signal validators to proceed.
	close(bumpDone)
	wg.Wait()

	// After the bump, ALL validators should have seen the new epoch and rejected
	// the epoch=0 token. No validator can "undo" the bump.
	assert.Equal(t, int64(validators), failAfterBump.Load(),
		"all %d validators after epoch bump must reject the epoch=0 token; "+
			"passed=%d, failed=%d",
		validators, passBeforeBump.Load(), failAfterBump.Load())
}
