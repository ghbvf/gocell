//go:build integration

// PR-V1-PG-ACCESSCORE-REPO B2.A Dev B + B5.FU(b) — service-level cross-store
// ACID integration test.
//
// Constructs sessionrefresh.Service with a real PGSessionRepository
// (cell-internal PG) and a real PGRefreshStore (adapter-layer PG) sharing
// a single TxManager, then exercises:
//
//   - TestStoreLevel_OuterTxAtomicity_SessionAndRefresh: store-level outer-TX
//     rollback (honest scope — does NOT call svc.Refresh(); proves the
//     underlying PG stores honor outer-TX semantics).
//
//   - TestService_Refresh_PG_HappyPath: end-to-end call to svc.Refresh(),
//     verifying that session.Update + refresh.Rotate both commit in one
//     atomic boundary and the returned TokenPair carries the expected fields
//     (B4 fix: proves sessionrefresh.Service.Refresh() works end-to-end with
//     real PG stores).
//
// ref: adapters/postgres/refresh_outer_tx_atomicity_integration_test.go
// ref: cells/accesscore/slices/sessionrefresh/service.go Refresh()
package sessionrefresh

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	cellpg "github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres/testfx"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

// errInjectedRollback is a sentinel error used to trigger outer-TX rollback
// in service-level integration tests.
var errInjectedRollback = errors.New("service-pg integration test: injected rollback")

// service-level integration test durations.
const (
	svcPgPolicyMaxAge        = 30 * 24 * time.Hour
	svcPgPolicyMaxIdle       = 7 * 24 * time.Hour
	svcPgPolicyReuseInterval = time.Second
)

// servicePGFixture holds all wired-up dependencies for a service-level PG test.
type servicePGFixture struct {
	svc          *Service
	sessionPG    *cellpg.PGSessionRepository
	refreshStore *adapterpg.PGRefreshStore
	txm          *adapterpg.TxManager
	pool         *adapterpg.Pool
	clock        *storetest.FakeClock
	userRepo     *mem.UserRepository
	roleRepo     *mem.RoleRepository
}

// newServicePGFixture builds a servicePGFixture using the shared base container
// + an isolated schema pool (B1/B2 fix: one container per test binary run).
func newServicePGFixture(t *testing.T) *servicePGFixture {
	t.Helper()

	pool := testfx.SetupPGPool(t)

	clk := storetest.NewFakeClock(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC))
	policy := refresh.Policy{
		MaxAge:         svcPgPolicyMaxAge,
		MaxIdle:        svcPgPolicyMaxIdle,
		ReuseInterval:  svcPgPolicyReuseInterval,
		GraceMaxReuses: 3,
	}
	require.NoError(t, policy.Validate())

	txm := adapterpg.NewTxManager(pool)

	sessionPG, err := cellpg.NewPGSessionRepository(pool.DB())
	require.NoError(t, err)

	refreshStore, err := adapterpg.NewRefreshStore(pool.DB(), txm, policy, clk, nil)
	require.NoError(t, err)

	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()

	// Build JWTIssuer using same key/audience as existing unit tests.
	keySet, _, _ := auth.MustNewTestKeySet(clock.Real())
	issuer, err := auth.NewJWTIssuer(keySet, "gocell-accesscore", auth.DefaultAccessTokenTTL, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)

	svc, err := NewService(sessionPG, roleRepo, userRepo, refreshStore, issuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(txm))
	require.NoError(t, err)

	return &servicePGFixture{
		svc:          svc,
		sessionPG:    sessionPG,
		refreshStore: refreshStore,
		txm:          txm,
		pool:         pool,
		clock:        clk,
		userRepo:     userRepo,
		roleRepo:     roleRepo,
	}
}

// TestStoreLevel_OuterTxAtomicity_SessionAndRefresh verifies store-level outer-TX
// rollback semantics: a RunInTx closure that manually performs session.Update +
// refresh.Rotate then returns an injected error must leave both stores unchanged.
//
// This test does NOT call svc.Refresh() — it directly exercises the underlying
// store layer. The name is intentionally honest about the scope (B4 fix:
// previous name "TestServicePG_Refresh_CommitFailure_RollsBackBothSessionAndRefreshRows"
// incorrectly implied service-level coverage).
//
// This is the B5.FU(b) "honest boundary" lifted: with real PGSessionRepository,
// the session.Update inside a RunInTx is now subject to outer-TX rollback.
func TestStoreLevel_OuterTxAtomicity_SessionAndRefresh(t *testing.T) {
	fx := newServicePGFixture(t)
	ctx := context.Background()

	userID := "user-svcpg-" + uuid.NewString()[:8]

	// Seed user so fetchPasswordResetRequired succeeds.
	u, err := domain.NewUser(userID, userID+"@test.local", "hash", time.Now())
	require.NoError(t, err)
	u.ID = userID
	require.NoError(t, fx.userRepo.Create(ctx, u))

	// Create session in PG.
	sess, err := domain.NewSession(userID, "original-at-"+uuid.NewString(), time.Now().Add(time.Hour), time.Now())
	require.NoError(t, err)
	sess.ID = "sess-svcpg-" + uuid.NewString()[:8]
	require.NoError(t, fx.sessionPG.Create(ctx, sess))

	originalAccessToken := sess.AccessToken
	originalVersion := sess.Version // = 1

	// Issue a refresh token for the session.
	wire, _, err := fx.refreshStore.Issue(ctx, sess.ID, userID)
	require.NoError(t, err)

	// Directly test TX rollback at the store layer (not through Refresh()).
	// This proves that the underlying PG stores honor outer-tx rollback semantics.
	var capturedRotatedWire string
	err = fx.txm.RunInTx(ctx, func(txCtx context.Context) error {
		// Simulate what Refresh does internally:
		// 1) update session (new access token)
		updSess := *sess
		updSess.AccessToken = "updated-at-" + uuid.NewString()
		if err := fx.sessionPG.Update(txCtx, &updSess); err != nil {
			return err
		}
		// 2) rotate refresh token
		rotatedWire, _, err := fx.refreshStore.Rotate(txCtx, wire)
		if err != nil {
			return err
		}
		capturedRotatedWire = rotatedWire
		// Inject rollback — simulates commit failure.
		return errInjectedRollback
	})
	require.ErrorIs(t, err, errInjectedRollback)
	require.NotEmpty(t, capturedRotatedWire)

	// Session must be unchanged (Update rolled back).
	gotSession, err := fx.sessionPG.GetByID(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, originalAccessToken, gotSession.AccessToken,
		"session.AccessToken must remain original after rollback")
	assert.Equal(t, originalVersion, gotSession.Version,
		"session.Version must remain %d after rollback", originalVersion)

	// Original refresh wire must remain valid (Rotate rolled back).
	tok, peekErr := fx.refreshStore.Peek(ctx, wire)
	require.NoError(t, peekErr, "original refresh wire must still be peekable after rollback")
	assert.Equal(t, sess.ID, tok.SessionID)

	// Rotated child must NOT be peekable.
	_, childPeekErr := fx.refreshStore.Peek(ctx, capturedRotatedWire)
	require.Error(t, childPeekErr, "rotated child must not be peekable after rollback")
	assert.True(t, errors.Is(childPeekErr, refresh.ErrRejected),
		"rotated child peek error must be ErrRejected (got %v)", childPeekErr)
}

// TestService_Refresh_PG_HappyPath verifies that svc.Refresh() successfully
// commits both the session.Update and refresh.Rotate atomically when given a
// valid refresh token backed by real PG stores.
//
// B4 fix: this is the first test that genuinely calls fx.svc.Refresh() end-to-
// end, proving that sessionrefresh.Service's internal RunInTx wraps session.Update
// + refresh.Rotate in one commit boundary and the returned TokenPair is coherent.
func TestService_Refresh_PG_HappyPath(t *testing.T) {
	fx := newServicePGFixture(t)
	ctx := context.Background()

	userID := "user-happy-" + uuid.NewString()[:8]

	// Seed user so fetchPasswordResetRequired succeeds.
	u, err := domain.NewUser(userID, userID+"@test.local", "hash", time.Now())
	require.NoError(t, err)
	u.ID = userID
	require.NoError(t, fx.userRepo.Create(ctx, u))

	// Create a session in PG.
	sess, err := domain.NewSession(userID, "at-happy-"+uuid.NewString(), time.Now().Add(time.Hour), time.Now())
	require.NoError(t, err)
	sess.ID = "sess-happy-" + uuid.NewString()[:8]
	require.NoError(t, fx.sessionPG.Create(ctx, sess))

	originalVersion := sess.Version

	// Issue a refresh token outside any transaction.
	wire, _, err := fx.refreshStore.Issue(ctx, sess.ID, userID)
	require.NoError(t, err)

	// Call the real service Refresh().
	pair, err := fx.svc.Refresh(ctx, wire)
	require.NoError(t, err, "Refresh must succeed with valid wire token + real PG stores")

	// TokenPair must carry the expected session/user context.
	assert.Equal(t, sess.ID, pair.SessionID, "returned SessionID must match the original session")
	assert.Equal(t, userID, pair.UserID, "returned UserID must match the seeded user")
	assert.NotEmpty(t, pair.AccessToken, "returned AccessToken must be non-empty")
	assert.NotEmpty(t, pair.RefreshToken, "returned RefreshToken must be non-empty")
	assert.NotEqual(t, wire, pair.RefreshToken, "rotated refresh token must differ from the original wire")

	// Session row must be updated (new access token, incremented version).
	gotSess, err := fx.sessionPG.GetByID(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, pair.AccessToken, gotSess.AccessToken,
		"session.AccessToken must match the newly issued token after Refresh")
	assert.Greater(t, gotSess.Version, originalVersion,
		"session.Version must be incremented after Refresh")

	// Original wire must be rotated (Peek should be rejected).
	_, peekErr := fx.refreshStore.Peek(ctx, wire)
	require.Error(t, peekErr, "original wire must be invalid after rotation")
	assert.True(t, errors.Is(peekErr, refresh.ErrRejected),
		"Peek on original wire after Refresh must return ErrRejected (got %v)", peekErr)

	// New wire must be peekable.
	newTok, newPeekErr := fx.refreshStore.Peek(ctx, pair.RefreshToken)
	require.NoError(t, newPeekErr, "new refresh wire from Refresh must be peekable")
	assert.Equal(t, sess.ID, newTok.SessionID, "new refresh token must be bound to the same session")
}
