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
//     while goroutines concurrently validate an access JWT issued at epoch=0.
//  2. Before the bump commits, validate calls on a valid active session succeed.
//  3. After the bump commits, all subsequent validate calls reject the epoch=0
//     token (401).
//  4. No panic occurs.
//
// The test deliberately splits validators into a "pre-bump" group that runs
// to completion before BumpAuthzEpoch is called, and a "post-bump" group that
// waits on a barrier and only runs once the bump has committed. Without the
// pre-bump pass coverage the earlier version of this test could not detect a
// regression that silently zeroed user.AuthzEpoch on read — the epoch check
// would still "fail" on every validate (claim=0 vs read=0 → match), but for
// the wrong reason. Asserting that pre-bump validators succeed plus post-bump
// validators all reject is the complete contract (Finding #9 PR #490 review).
func TestSessionvalidate_ConcurrentEpochBumpAndValidate(t *testing.T) {
	const validatorsPre, validatorsPost = 25, 25

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

	// Pre-bump phase: validators run to completion before any bump occurs.
	// Each must succeed because user.AuthzEpoch (read from repo) == claim epoch (0).
	var preWG sync.WaitGroup
	var preBumpPass, preBumpFail atomic.Int64
	for i := 0; i < validatorsPre; i++ {
		preWG.Add(1)
		go func() {
			defer preWG.Done()
			_, vErr := svc.VerifyIntent(context.Background(), tokenStr, auth.TokenIntentAccess)
			if vErr == nil {
				preBumpPass.Add(1)
				return
			}
			preBumpFail.Add(1)
		}()
	}
	preWG.Wait()
	assert.Equal(t, int64(validatorsPre), preBumpPass.Load(),
		"pre-bump validators must all PASS (claim epoch=0 matches user.AuthzEpoch=0); "+
			"if any failed, the read path is silently dropping authz_epoch — see Finding #1")
	assert.Equal(t, int64(0), preBumpFail.Load(), "no pre-bump validator should have failed")

	// Post-bump phase: spin up validators that block on a barrier, then bump
	// the epoch, then release them. All must reject the now-stale token.
	bumpDone := make(chan struct{})
	var postWG sync.WaitGroup
	var postValidatorsReady sync.WaitGroup
	postValidatorsReady.Add(validatorsPost)
	var postBumpPass, postBumpFail atomic.Int64

	for i := 0; i < validatorsPost; i++ {
		postWG.Add(1)
		go func() {
			defer postWG.Done()
			postValidatorsReady.Done()
			<-bumpDone

			_, vErr := svc.VerifyIntent(context.Background(), tokenStr, auth.TokenIntentAccess)
			if vErr != nil {
				postBumpFail.Add(1)
			} else {
				postBumpPass.Add(1)
			}
		}()
	}
	postValidatorsReady.Wait()

	// Bump the epoch directly (test-only; funnel not needed here — see package
	// godoc for rationale).
	_, err = userRepo.BumpAuthzEpoch(context.Background(), userID)
	require.NoError(t, err, "BumpAuthzEpoch must succeed")

	close(bumpDone)
	postWG.Wait()

	assert.Equal(t, int64(validatorsPost), postBumpFail.Load(),
		"all %d post-bump validators must reject the epoch=0 token; "+
			"passed=%d, failed=%d (a regression that drops authz_epoch from the read path "+
			"would let claim=0 silently match user.AuthzEpoch=0 here too)",
		validatorsPost, postBumpPass.Load(), postBumpFail.Load())
}
