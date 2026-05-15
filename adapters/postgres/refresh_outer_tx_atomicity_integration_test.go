//go:build integration

// PR-V1-PG-REFRESH-CROSS-STORE B5 — outer-tx atomicity proof for the PG
// refresh store + TxManager combination that backs sessionrefresh.Service's
// cross-store ACID wrap.
//
// The B5 service-layer fix wraps the validate→update→rotate sequence in
// sessionrefresh.Service.Refresh() inside one txRunner.RunInTx call. That
// archtest (REFRESH-CROSS-STORE-TX-01) statically locks the wrap. This file
// provides the dynamic counterpart: it exercises the underlying TX mechanics
// directly so we know the wrap actually delivers what it claims.
//
// Three failure modes mirror the sessionrefresh.Refresh() code path:
//  1. Issue inside an outer RunInTx that aborts — the new chain row must be
//     rolled back. (Sanity check on the savepoint nesting / write-back paths.)
//  2. Rotate inside an outer RunInTx that aborts after Rotate — the rotated
//     child must NOT be visible and the parent must remain unrotated. This is
//     the "Rotate fails after session.Update succeeds" scenario in service-
//     layer terms.
//  3. RevokeSessionDetached inside an outer RunInTx that aborts — the revoke
//     MUST persist regardless of the outer rollback (PR#395 detached-context
//     invariant). Locks B5's interaction with cascade-revoke paths.
//
// Honest test-scope boundary: this proves PG refresh-side rollback semantics
// under the outer wrap. Session-side rollback assertion is held until B2
// lands postgres.PGSessionRepository (the wired session repo today is mem,
// which doesn't honor TX rollback by design).
//
// ref: jackc/pgx tx savepoint nesting; ory/fosite refresh.Store contract.
package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

// b5 test policy durations (TEST-TIME-LITERAL-01: extract literals to
// package-level consts).
const (
	b5PolicyMaxAge        = 30 * 24 * time.Hour
	b5PolicyMaxIdle       = 7 * 24 * time.Hour
	b5PolicyReuseInterval = time.Second
)

// errInjectedRollback is a sentinel returned by the test closure to trigger
// outer-tx rollback. It must not match any real refresh.Store error so the
// rollback intent is unambiguous.
var errInjectedRollback = errors.New("b5 test: injected rollback after refresh-store mutation")

// b5Fixture wires PG TxManager + PGRefreshStore and is reused across the
// three subtests. Each subtest gets an isolated PG schema so refresh_tokens
// rows from one subtest never leak into another.
type b5Fixture struct {
	store    *PGRefreshStore
	txm      *TxManager
	pool     *Pool
	clock    *storetest.FakeClock
	policyOK refresh.Policy
}

func newB5Fixture(t *testing.T, base *Pool) *b5Fixture {
	t.Helper()
	ctx := context.Background()

	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clk := storetest.NewFakeClock(time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC))
	policy := refresh.Policy{
		MaxAge:         b5PolicyMaxAge,
		MaxIdle:        b5PolicyMaxIdle,
		ReuseInterval:  b5PolicyReuseInterval,
		GraceMaxReuses: 3,
	}
	require.NoError(t, policy.Validate())

	txm := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm, policy, clk, nil)
	require.NoError(t, err)
	return &b5Fixture{
		store:    store,
		txm:      txm,
		pool:     p,
		clock:    clk,
		policyOK: policy,
	}
}

// TestB5_OuterTxRollback_IssueRolledBack covers scenario 1: an Issue inside
// an outer RunInTx that aborts must leave the refresh table in its
// pre-Issue state. The wire token returned by Issue must be unpeekable
// after the outer tx rolls back.
func TestB5_OuterTxRollback_IssueRolledBack(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)
	fx := newB5Fixture(t, base)
	ctx := context.Background()

	const sessionID = "sess-b5-issue"
	const subjectID = "usr-b5-issue"

	var capturedWire string
	err := fx.txm.RunInTx(ctx, func(txCtx context.Context) error {
		wire, _, ierr := fx.store.Issue(txCtx, sessionID, subjectID, int64(1))
		if ierr != nil {
			return ierr
		}
		capturedWire = wire
		return errInjectedRollback
	})
	require.ErrorIs(t, err, errInjectedRollback, "outer tx must propagate the injected error")
	require.NotEmpty(t, capturedWire, "Issue should have produced a wire token before rollback")

	// After rollback the wire token must NOT be peekable; the row was never
	// committed.
	_, peekErr := fx.store.Peek(ctx, capturedWire)
	require.Error(t, peekErr, "Peek must reject a rolled-back wire token")
	assert.True(t, errors.Is(peekErr, refresh.ErrRejected),
		"Peek error after Issue rollback must be refresh.ErrRejected (got %v)", peekErr)
}

// TestB5_OuterTxRollback_RotateRolledBack covers scenario 2: Issue commits
// outside the wrap; then a Rotate happens inside an outer RunInTx that
// aborts after Rotate. The rotation must be undone — the original wire is
// still peekable, the rotated child is not.
//
// This mirrors the headline B5 case: a session.Update succeeds, then Rotate
// runs (its savepoint), then a downstream operation fails (here, our
// injected error). With the cross-store wrap in place, the entire outer tx
// rolls back and Rotate's effect is undone.
func TestB5_OuterTxRollback_RotateRolledBack(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)
	fx := newB5Fixture(t, base)
	ctx := context.Background()

	const sessionID = "sess-b5-rotate"
	const subjectID = "usr-b5-rotate"

	originalWire, _, err := fx.store.Issue(ctx, sessionID, subjectID, int64(1))
	require.NoError(t, err)

	var rotatedWire string
	err = fx.txm.RunInTx(ctx, func(txCtx context.Context) error {
		newWire, _, rerr := fx.store.Rotate(txCtx, originalWire)
		if rerr != nil {
			return rerr
		}
		rotatedWire = newWire
		return errInjectedRollback
	})
	require.ErrorIs(t, err, errInjectedRollback)
	require.NotEmpty(t, rotatedWire, "Rotate should have produced a child wire before rollback")

	// Original token must still be peekable — Rotate's flip of parent.rotated_at
	// and the child INSERT were both rolled back.
	tok, peekErr := fx.store.Peek(ctx, originalWire)
	require.NoError(t, peekErr,
		"Peek on the original wire must succeed after outer-tx rollback; Rotate's effect was undone")
	assert.Equal(t, sessionID, tok.SessionID)
	assert.Equal(t, subjectID, tok.SubjectID)

	// The rotated child must NOT be peekable — its INSERT was rolled back.
	_, childPeekErr := fx.store.Peek(ctx, rotatedWire)
	require.Error(t, childPeekErr, "Peek on the rolled-back rotated child must reject")
	assert.True(t, errors.Is(childPeekErr, refresh.ErrRejected),
		"Peek error on rolled-back child must be refresh.ErrRejected (got %v)", childPeekErr)
}

// TestB5_RevokeSessionDetachedSurvivesOuterRollback covers scenario 3: a
// RevokeSessionDetached call inside an outer RunInTx that aborts MUST
// commit independently. PR#395 made cascade-revoke detached precisely so
// security-response writes survive the surrounding business-tx rollback.
//
// Test sequence: Issue commits outside the wrap; outer RunInTx calls
// RevokeSessionDetached and then injects an error. Outer rollback fires.
// Afterwards Peek on the original wire MUST reject (revoke persisted).
func TestB5_RevokeSessionDetachedSurvivesOuterRollback(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)
	fx := newB5Fixture(t, base)
	ctx := context.Background()

	const sessionID = "sess-b5-cascade"
	const subjectID = "usr-b5-cascade"

	wire, _, err := fx.store.Issue(ctx, sessionID, subjectID, int64(1))
	require.NoError(t, err)

	// Sanity check: token is alive before the cascade.
	_, err = fx.store.Peek(ctx, wire)
	require.NoError(t, err)

	err = fx.txm.RunInTx(ctx, func(txCtx context.Context) error {
		if cerr := fx.store.RevokeSessionDetached(txCtx, sessionID); cerr != nil {
			return cerr
		}
		return errInjectedRollback
	})
	require.ErrorIs(t, err, errInjectedRollback, "outer tx must propagate injected error")

	// Detached revoke must have persisted despite outer rollback.
	_, peekErr := fx.store.Peek(ctx, wire)
	require.Error(t, peekErr,
		"after RevokeSessionDetached the token must reject even though the outer tx rolled back")
	assert.True(t, errors.Is(peekErr, refresh.ErrRejected),
		"Peek error after detached revoke must be refresh.ErrRejected (got %v)", peekErr)
}
