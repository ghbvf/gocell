package sessionvalidate

// sessionvalidate_concurrent_epoch_race_test.go — concurrent epoch-bump vs
// validate race test.
//
// S4d change: epoch comparison now uses user.AuthzEpoch vs view.AuthzEpochAtIssue
// (session row), not the JWT claim. This test guards the race safety of that
// read-path: one goroutine bumps the epoch directly via userRepo.BumpAuthzEpoch
// (bypassing the funnel to isolate the test scope to sessionvalidate only),
// while 50 goroutines concurrently validate an access JWT.
//
// Design:
//  1. Create user with AuthzEpoch=1. Create session with AuthzEpochAtIssue=1.
//  2. Pre-bump phase: 25 validators must PASS (user.epoch=1 == session.epoch=1).
//  3. Bump user.epoch to 2 via userRepo.BumpAuthzEpoch.
//  4. Post-bump phase: 25 validators must FAIL (user.epoch=2 != session.epoch=1).
//  5. No panic from concurrent reads.
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
//     while goroutines concurrently validate an access JWT.
//  2. Before the bump commits, validate calls on a valid active session succeed
//     (user.epoch=1 matches session.AuthzEpochAtIssue=1).
//  3. After the bump commits, all subsequent validate calls reject the session
//     (user.epoch=2 != session.AuthzEpochAtIssue=1).
//  4. No panic occurs.
//
// The test deliberately splits validators into a "pre-bump" group that runs
// to completion before BumpAuthzEpoch is called, and a "post-bump" group that
// waits on a barrier and only runs once the bump has committed. Without the
// pre-bump pass coverage, a regression that silently zeroes user.AuthzEpoch
// on read would cause pre-bump to fail (0 != 1) — making the regression
// detectable via the pre-bump PASS assertion (Finding #9 PR #490 review).
func TestSessionvalidate_ConcurrentEpochBumpAndValidate(t *testing.T) {
	const validatorsPre, validatorsPost = 25, 25

	memStore := mem.NewStore(clock.Real())
	userRepo := memStore.UserRepository()
	sessionStore := newTestStore(t)

	// S4d: user starts at epoch=1 (domain.NewUser sets AuthzEpoch=1).
	userID := "race-epoch-" + uuid.NewString()[:8]
	sessionID := "race-sess-" + uuid.NewString()[:8]
	initialUser := &domain.User{
		ID:         userID,
		Username:   "race-epoch-user",
		Email:      "race@epoch.test",
		Status:     domain.StatusActive,
		AuthzEpoch: 1,
		CreatedAt:  clock.Real().Now(),
		UpdatedAt:  clock.Real().Now(),
	}
	require.NoError(t, userRepo.Create(context.Background(), initialUser))

	// Seed an active session with AuthzEpochAtIssue=1 — matches user.epoch=1.
	require.NoError(t, sessionStore.Create(context.Background(), &session.Session{
		ID:                sessionID,
		SubjectID:         userID,
		JTI:               "race-jti-" + sessionID,
		AuthzEpochAtIssue: 1,
		CreatedAt:         clock.Real().Now(),
		ExpiresAt:         clock.Real().Now().Add(time.Hour),
	}))

	svc, err := NewService(testVerifier, sessionStore, userRepo, slog.Default())
	require.NoError(t, err)

	// Issue a token bound to the session.
	tokenStr, err := IssueTestToken(testPrivKey, userID, nil, time.Hour, sessionID)
	require.NoError(t, err)

	// Pre-bump phase: validators run to completion before any bump occurs.
	// Each must succeed: user.AuthzEpoch=1 == session.AuthzEpochAtIssue=1.
	// If the read path silently drops AuthzEpoch (returning 0), then 0 != 1
	// fails here — the regression is caught by the PASS assertion below.
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
		"pre-bump validators must all PASS (user.AuthzEpoch=1 == session.AuthzEpochAtIssue=1); "+
			"if any failed, the read path is silently dropping authz_epoch — see Finding #1")
	assert.Equal(t, int64(0), preBumpFail.Load(), "no pre-bump validator should have failed")

	// Post-bump phase: spin up validators that block on a barrier, then bump
	// the epoch, then release them. All must reject: user.epoch=2 != session.epoch=1.
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
	newEpoch, err := userRepo.BumpAuthzEpoch(context.Background(), userID)
	require.NoError(t, err, "BumpAuthzEpoch must succeed")
	require.Equal(t, int64(2), newEpoch, "bump from epoch=1 must yield epoch=2")

	close(bumpDone)
	postWG.Wait()

	assert.Equal(t, int64(validatorsPost), postBumpFail.Load(),
		"all %d post-bump validators must reject (user.epoch=2 != session.epoch=1); "+
			"passed=%d, failed=%d (a regression that drops authz_epoch from the read path "+
			"would let user.epoch=0 silently mismatch session.epoch=1 already in pre-bump)",
		validatorsPost, postBumpPass.Load(), postBumpFail.Load())
}
