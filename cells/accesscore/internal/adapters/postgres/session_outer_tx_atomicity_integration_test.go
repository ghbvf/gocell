//go:build integration

// PR-V1-PG-ACCESSCORE-REPO B2.A Dev B — cross-store outer-TX atomicity tests.
//
// Exercises the cell-internal PGSessionRepository + adapters/postgres
// PGRefreshStore sharing a single outer transaction via TxManager.RunInTx.
// The two stores join the outer TX through the ambient persistence.TxCtxKey,
// so a rollback undoes writes from both stores atomically.
//
// Three failure modes:
//  1. BothCommit — RunInTx closure returns nil; both rows are visible.
//  2. BothRollback — closure returns an injected error; both rows disappear.
//  3. RotateFails_SessionUpdateRollback — session.Update succeeds inside the
//     outer TX; then Rotate fails (injected error); the outer rollback undoes
//     session.Update as well.
//
// Honest test-scope boundary: completes the "session-side rollback assertion"
// deferred in adapters/postgres/refresh_outer_tx_atomicity_integration_test.go
// ("Honest test-scope boundary" comment) that was held until B2 lands
// PGSessionRepository.
//
// ref: adapters/postgres/refresh_outer_tx_atomicity_integration_test.go (b5Fixture pattern)
// ref: jackc/pgx tx savepoint nesting
package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

// cross-store test durations (TEST-TIME-LITERAL-01: extract to package-level consts).
const (
	outerTxPolicyMaxAge        = 30 * 24 * time.Hour
	outerTxPolicyMaxIdle       = 7 * 24 * time.Hour
	outerTxPolicyReuseInterval = time.Second
)

// errInjectedSessionRollback is a sentinel returned by the test closure to
// trigger outer-tx rollback without matching any real error.
var errInjectedSessionRollback = errors.New("cross-store test: injected rollback after session mutation")

// crossStoreFixture wires PGSessionRepository (cell-internal) + PGRefreshStore
// (adapter layer) sharing a single Pool and TxManager.
type crossStoreFixture struct {
	sessionRepo  *PGSessionRepository
	refreshStore *adapterpg.PGRefreshStore
	txm          *adapterpg.TxManager
	pool         *adapterpg.Pool
	clock        *storetest.FakeClock
}

// newCrossStoreFixture builds a crossStoreFixture using the shared base
// container + an isolated schema pool (B1 fix: one container per test run).
func newCrossStoreFixture(t *testing.T) *crossStoreFixture {
	t.Helper()

	pool := setupPGPool(t)
	ctx := context.Background()

	clk := storetest.NewFakeClock(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC))
	policy := refresh.Policy{
		MaxAge:         outerTxPolicyMaxAge,
		MaxIdle:        outerTxPolicyMaxIdle,
		ReuseInterval:  outerTxPolicyReuseInterval,
		GraceMaxReuses: 3,
	}
	require.NoError(t, policy.Validate())

	txm := adapterpg.NewTxManager(pool)

	sessionRepo, err := NewPGSessionRepository(pool.DB(), clock.Real())
	require.NoError(t, err)

	refreshStore, err := adapterpg.NewRefreshStore(pool.DB(), txm, policy, clk, nil)
	require.NoError(t, err)

	_ = ctx // pool cleanup is registered by setupPGPool via t.Cleanup

	return &crossStoreFixture{
		sessionRepo:  sessionRepo,
		refreshStore: refreshStore,
		txm:          txm,
		pool:         pool,
		clock:        clk,
	}
}

// newCrossStoreTestSession builds a domain.Session suitable for cross-store tests.
func newCrossStoreTestSession(sessionID, userID, accessToken string) *domain.Session {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Session{
		ID:          sessionID,
		UserID:      userID,
		AccessToken: accessToken,
		ExpiresAt:   now.Add(time.Hour),
		CreatedAt:   now,
		Version:     1,
	}
}

// TestCrossStore_SessionAndRefresh_BothCommit verifies that when the outer
// RunInTx closure returns nil, both the session row and the refresh token row
// are visible after the transaction commits.
func TestCrossStore_SessionAndRefresh_BothCommit(t *testing.T) {
	fx := newCrossStoreFixture(t)
	ctx := context.Background()

	sessionID := "sess-commit-" + uuid.NewString()[:8]
	userID := "user-commit-" + uuid.NewString()[:8]

	s := newCrossStoreTestSession(sessionID, userID, "tok-commit-"+uuid.NewString())

	var capturedWire string
	err := fx.txm.RunInTx(ctx, func(txCtx context.Context) error {
		if err := fx.sessionRepo.Create(txCtx, s); err != nil {
			return err
		}
		wire, _, err := fx.refreshStore.Issue(txCtx, sessionID, userID)
		if err != nil {
			return err
		}
		capturedWire = wire
		return nil
	})
	require.NoError(t, err, "outer tx must commit cleanly")

	// Session row must be visible.
	got, err := fx.sessionRepo.GetByID(ctx, sessionID)
	require.NoError(t, err, "session must be visible after commit")
	assert.Equal(t, sessionID, got.ID)

	// Refresh token must be peekable.
	tok, err := fx.refreshStore.Peek(ctx, capturedWire)
	require.NoError(t, err, "refresh token must be peekable after commit")
	assert.Equal(t, sessionID, tok.SessionID)
}

// TestCrossStore_SessionAndRefresh_BothRollback verifies that when the outer
// RunInTx closure returns an error, both the session row and the refresh token
// row are rolled back and not visible.
func TestCrossStore_SessionAndRefresh_BothRollback(t *testing.T) {
	fx := newCrossStoreFixture(t)
	ctx := context.Background()

	sessionID := "sess-rb-" + uuid.NewString()[:8]
	userID := "user-rb-" + uuid.NewString()[:8]

	s := newCrossStoreTestSession(sessionID, userID, "tok-rb-"+uuid.NewString())

	var capturedWire string
	err := fx.txm.RunInTx(ctx, func(txCtx context.Context) error {
		if err := fx.sessionRepo.Create(txCtx, s); err != nil {
			return err
		}
		wire, _, err := fx.refreshStore.Issue(txCtx, sessionID, userID)
		if err != nil {
			return err
		}
		capturedWire = wire
		return errInjectedSessionRollback
	})
	require.ErrorIs(t, err, errInjectedSessionRollback)
	require.NotEmpty(t, capturedWire, "Issue should have produced a wire token before rollback")

	// Session row must NOT be visible.
	_, sessionErr := fx.sessionRepo.GetByID(ctx, sessionID)
	require.Error(t, sessionErr, "session must not be visible after outer rollback")

	// Refresh token must NOT be peekable.
	_, peekErr := fx.refreshStore.Peek(ctx, capturedWire)
	require.Error(t, peekErr, "refresh token must not be peekable after outer rollback")
	assert.True(t, errors.Is(peekErr, refresh.ErrRejected),
		"Peek error after rollback must be refresh.ErrRejected (got %v)", peekErr)
}

// TestCrossStore_RotateFails_SessionUpdateRollback verifies that when a Rotate
// fails (injected error) after a successful session.Update inside the outer
// RunInTx, both the session update and the rotate attempt are rolled back.
//
// This mirrors the sessionrefresh.Service.Refresh() code path:
// persistRefreshedSession (session.Update) runs before Rotate; if anything
// after the Update fails, the outer TX aborts and the Update is undone.
func TestCrossStore_RotateFails_SessionUpdateRollback(t *testing.T) {
	fx := newCrossStoreFixture(t)
	ctx := context.Background()

	sessionID := "sess-rotfail-" + uuid.NewString()[:8]
	userID := "user-rotfail-" + uuid.NewString()[:8]
	originalToken := "tok-rotfail-" + uuid.NewString()

	// Set up: create session and issue refresh token outside the outer TX.
	s := newCrossStoreTestSession(sessionID, userID, originalToken)
	require.NoError(t, fx.sessionRepo.Create(ctx, s))

	originalWire, _, err := fx.refreshStore.Issue(ctx, sessionID, userID)
	require.NoError(t, err)

	// Capture version before the TX.
	originalVersion := s.Version // = 1

	newToken := "tok-rotfail-updated-" + uuid.NewString()

	// Run a TX: update session, rotate — inject error to trigger rollback.
	updSession := *s
	updSession.AccessToken = newToken
	// Version matches the current DB value (1).
	updSession.Version = originalVersion

	err = fx.txm.RunInTx(ctx, func(txCtx context.Context) error {
		if err := fx.sessionRepo.Update(txCtx, &updSession); err != nil {
			return err
		}
		_, _, err := fx.refreshStore.Rotate(txCtx, originalWire)
		if err != nil {
			return err
		}
		// Inject rollback after both Update and Rotate succeed.
		return errInjectedSessionRollback
	})
	require.ErrorIs(t, err, errInjectedSessionRollback)

	// Session.AccessToken must be the original value (Update rolled back).
	got, err := fx.sessionRepo.GetByID(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, originalToken, got.AccessToken,
		"session.AccessToken must remain unchanged after rollback of Update")
	assert.Equal(t, originalVersion, got.Version,
		"session.Version must remain %d after rollback of Update", originalVersion)

	// Original refresh wire must still be peekable (Rotate rolled back).
	tok, peekErr := fx.refreshStore.Peek(ctx, originalWire)
	require.NoError(t, peekErr,
		"original refresh wire must be peekable after Rotate rollback")
	assert.Equal(t, sessionID, tok.SessionID)
}
