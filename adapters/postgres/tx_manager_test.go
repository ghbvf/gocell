package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// mockTx implements pgx.Tx for unit testing.
type mockTx struct {
	pgx.Tx
	committed  bool
	rolledBack bool
	execCalls  []string
	// rollbackCtxCancelled records whether the context passed to Rollback was already canceled.
	rollbackCtxCancelled bool
	// execCtxCancelled tracks per-call whether the context was canceled (parallel to execCalls).
	execCtxCancelled []bool
}

func (m *mockTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.execCalls = append(m.execCalls, sql)
	m.execCtxCancelled = append(m.execCtxCancelled, ctx.Err() != nil)
	return pgconn.NewCommandTag(""), nil
}

func (m *mockTx) Commit(ctx context.Context) error {
	m.committed = true
	return nil
}

func (m *mockTx) Rollback(ctx context.Context) error {
	m.rolledBack = true
	m.rollbackCtxCancelled = ctx.Err() != nil
	return nil
}

func TestCtxWithTx_RoundTrip(t *testing.T) {
	ctx := context.Background()

	// No tx in fresh context.
	tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
	assert.False(t, ok)
	assert.Nil(t, tx)

	// Store and retrieve.
	mock := &mockTx{}
	ctx = CtxWithTx(ctx, mock)
	tx, ok = persistence.TxFromContext[pgx.Tx](ctx)
	assert.True(t, ok)
	assert.Same(t, mock, tx)
}

func TestTxFromContext_NilContext(t *testing.T) {
	tx, ok := persistence.TxFromContext[pgx.Tx](context.Background())
	assert.False(t, ok)
	assert.Nil(t, tx)
}

func TestSavepointDepth(t *testing.T) {
	ctx := context.Background()

	// Default depth is 0.
	assert.Equal(t, 0, savepointDepth(ctx))

	// Set and read depth.
	ctx = withSavepointDepth(ctx, 3)
	assert.Equal(t, 3, savepointDepth(ctx))
}

func TestSavepointDepth_Nesting(t *testing.T) {
	ctx := context.Background()
	ctx1 := withSavepointDepth(ctx, 1)
	ctx2 := withSavepointDepth(ctx1, 2)

	// Parent contexts are unaffected.
	assert.Equal(t, 0, savepointDepth(ctx))
	assert.Equal(t, 1, savepointDepth(ctx1))
	assert.Equal(t, 2, savepointDepth(ctx2))
}

func TestNewTxManager(t *testing.T) {
	// NewTxManager requires a Pool with a non-nil inner.
	// We can't create a real pool without a DB, so just verify nil-safety of
	// the constructor path by checking it doesn't panic with a valid Pool stub.
	p := &Pool{inner: nil}
	tm := NewTxManager(p)
	require.NotNil(t, tm)
}

// TestRunInTx_Savepoint_DoesNotDrainAfterCommit verifies the savepoint
// (nested) path never drains after-commit hooks: only the outermost top-level
// commit fires them, so a savepoint RELEASE must leave hooks pending. Uses a
// mockTx so no real pool is needed.
func TestRunInTx_Savepoint_DoesNotDrainAfterCommit(t *testing.T) {
	mock := &mockTx{}
	ctx := CtxWithTx(context.Background(), mock)
	ctx = withSavepointDepth(ctx, 0)
	// Simulate an outer top-level RunInTx having installed the registry.
	ctx, top := persistence.WithAfterCommitRegistry(ctx)
	require.True(t, top)

	tm := &TxManager{pool: nil} // savepoint path never touches the pool

	fired := false
	err := tm.RunInTx(ctx, func(inner context.Context) error {
		persistence.RegisterAfterCommit(inner, func(context.Context) { fired = true })
		return nil
	})
	require.NoError(t, err)
	assert.False(t, fired,
		"savepoint RELEASE must not drain after-commit hooks; only the outermost commit does")

	// The hook is still pending in the outer registry — the outer RunInTx would
	// drain it after its real Commit.
	persistence.RunAfterCommitHooks(ctx)
	assert.True(t, fired, "outer drain fires the hook registered inside the savepoint")
}

// TestRunInTx_Savepoint_RollbackDiscardsHooks verifies that a savepoint rollback
// (nested fn error) discards hooks registered inside that scope, so an outer
// scope that swallows the error and drains does not fire them.
func TestRunInTx_Savepoint_RollbackDiscardsHooks(t *testing.T) {
	mock := &mockTx{}
	ctx := CtxWithTx(context.Background(), mock)
	ctx = withSavepointDepth(ctx, 0)
	ctx, top := persistence.WithAfterCommitRegistry(ctx)
	require.True(t, top)

	tm := &TxManager{pool: nil}

	nestedFired := false
	sentinel := errors.New("nested unit failed")
	err := tm.RunInTx(ctx, func(inner context.Context) error {
		persistence.RegisterAfterCommit(inner, func(context.Context) { nestedFired = true })
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, "ROLLBACK TO SAVEPOINT sp_0", mock.execCalls[len(mock.execCalls)-1])

	// Outer swallows the error and drains: the rolled-back scope's hook must be gone.
	persistence.RunAfterCommitHooks(ctx)
	assert.False(t, nestedFired, "savepoint rollback must discard the nested scope's after-commit hooks")
}

func TestRunInTx_Savepoint_ExecSequence(t *testing.T) {
	// Simulate a nested call: context already has a tx.
	mock := &mockTx{}
	ctx := CtxWithTx(context.Background(), mock)
	ctx = withSavepointDepth(ctx, 0)

	// TxManager with nil pool is fine because we won't call pool.Begin.
	tm := &TxManager{pool: nil}

	err := tm.RunInTx(ctx, func(innerCtx context.Context) error {
		// Should be at depth 1 now.
		assert.Equal(t, 1, savepointDepth(innerCtx))

		// The tx in context should be the same mock.
		tx, ok := persistence.TxFromContext[pgx.Tx](innerCtx)
		assert.True(t, ok)
		assert.Same(t, mock, tx)
		return nil
	})
	require.NoError(t, err)

	// Verify SAVEPOINT was created and released.
	require.Len(t, mock.execCalls, 2)
	assert.Equal(t, "SAVEPOINT sp_0", mock.execCalls[0])
	assert.Equal(t, "RELEASE SAVEPOINT sp_0", mock.execCalls[1])
}

func TestRunInTx_Savepoint_Rollback_OnError(t *testing.T) {
	mock := &mockTx{}
	ctx := CtxWithTx(context.Background(), mock)
	ctx = withSavepointDepth(ctx, 0)

	tm := &TxManager{pool: nil}

	testErr := assert.AnError
	err := tm.RunInTx(ctx, func(_ context.Context) error {
		return testErr
	})
	require.ErrorIs(t, err, testErr)

	// Verify SAVEPOINT was created and rolled back.
	require.Len(t, mock.execCalls, 2)
	assert.Equal(t, "SAVEPOINT sp_0", mock.execCalls[0])
	assert.Equal(t, "ROLLBACK TO SAVEPOINT sp_0", mock.execCalls[1])
}

func TestRunInTx_Savepoint_Rollback_OnPanic(t *testing.T) {
	mock := &mockTx{}
	ctx := CtxWithTx(context.Background(), mock)
	ctx = withSavepointDepth(ctx, 0)

	tm := &TxManager{pool: nil}

	assert.PanicsWithValue(t, "test panic", func() {
		_ = tm.RunInTx(ctx, func(_ context.Context) error {
			panic("test panic")
		})
	})

	// Verify SAVEPOINT was created and rolled back on panic.
	require.Len(t, mock.execCalls, 2)
	assert.Equal(t, "SAVEPOINT sp_0", mock.execCalls[0])
	assert.Equal(t, "ROLLBACK TO SAVEPOINT sp_0", mock.execCalls[1])
}

func TestRunInTx_NestedSavepoints(t *testing.T) {
	mock := &mockTx{}
	ctx := CtxWithTx(context.Background(), mock)
	ctx = withSavepointDepth(ctx, 0)

	tm := &TxManager{pool: nil}

	err := tm.RunInTx(ctx, func(ctx1 context.Context) error {
		assert.Equal(t, 1, savepointDepth(ctx1))
		return tm.RunInTx(ctx1, func(ctx2 context.Context) error {
			assert.Equal(t, 2, savepointDepth(ctx2))
			return nil
		})
	})
	require.NoError(t, err)

	// Expect: SAVEPOINT sp_0, SAVEPOINT sp_1, RELEASE sp_1, RELEASE sp_0
	require.Len(t, mock.execCalls, 4)
	assert.Equal(t, "SAVEPOINT sp_0", mock.execCalls[0])
	assert.Equal(t, "SAVEPOINT sp_1", mock.execCalls[1])
	assert.Equal(t, "RELEASE SAVEPOINT sp_1", mock.execCalls[2])
	assert.Equal(t, "RELEASE SAVEPOINT sp_0", mock.execCalls[3])
}

// --- Tests for P0 fix: rollback must use context.WithoutCancel ---

func TestRunInTx_Savepoint_Rollback_WithCancelledCtx(t *testing.T) {
	// Verify that savepoint rollback on error uses an uncancelled context
	// even when the caller context is already canceled.
	mock := &mockTx{}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = CtxWithTx(ctx, mock)
	ctx = withSavepointDepth(ctx, 0)

	tm := &TxManager{pool: nil}

	testErr := assert.AnError
	err := tm.RunInTx(ctx, func(_ context.Context) error {
		// Simulate HTTP timeout: cancel the parent context before returning error.
		cancel()
		return testErr
	})
	require.ErrorIs(t, err, testErr)

	// Verify SAVEPOINT was created and rolled back.
	require.Len(t, mock.execCalls, 2)
	assert.Equal(t, "SAVEPOINT sp_0", mock.execCalls[0])
	assert.Equal(t, "ROLLBACK TO SAVEPOINT sp_0", mock.execCalls[1])

	// The rollback Exec call must NOT have seen a canceled context
	// (context.WithoutCancel strips the cancellation signal).
	require.Len(t, mock.execCtxCancelled, 2)
	assert.False(t, mock.execCtxCancelled[1],
		"savepoint rollback must use an uncancelled context (context.WithoutCancel)")
}

func TestRunInTx_Savepoint_Rollback_OnPanic_WithCancelledCtx(t *testing.T) {
	// Verify that savepoint rollback on panic uses an uncancelled context.
	mock := &mockTx{}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = CtxWithTx(ctx, mock)
	ctx = withSavepointDepth(ctx, 0)

	tm := &TxManager{pool: nil}

	assert.PanicsWithValue(t, "timeout panic", func() {
		_ = tm.RunInTx(ctx, func(_ context.Context) error {
			cancel() // context canceled before panic
			panic("timeout panic")
		})
	})

	// Verify savepoint was created and rolled back.
	require.Len(t, mock.execCalls, 2)
	assert.Equal(t, "SAVEPOINT sp_0", mock.execCalls[0])
	assert.Equal(t, "ROLLBACK TO SAVEPOINT sp_0", mock.execCalls[1])

	// The rollback must use an uncancelled context.
	require.Len(t, mock.execCtxCancelled, 2)
	assert.False(t, mock.execCtxCancelled[1],
		"savepoint rollback on panic must use an uncancelled context")
}

// --- Tests for ApplyTenantScope ---

// TestApplyTenantScope_NoAmbientTx verifies that ApplyTenantScope returns a
// KindInternal error when there is no ambient pgx.Tx in the context (i.e. when
// called outside RunInTx). This enforces the fail-closed contract: the GUC is
// never written without an open transaction.
func TestApplyTenantScope_NoAmbientTx(t *testing.T) {
	tm := &TxManager{pool: nil}
	// No CtxWithTx — context carries no transaction.
	err := tm.ApplyTenantScope(context.Background(), "00000000-0000-0000-0000-000000000001")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInternal, ecErr.Kind,
		"ApplyTenantScope without ambient tx must return KindInternal")
}

// TestApplyTenantScope_InvalidUUID verifies that ApplyTenantScope returns a
// KindInternal error when the tenant string is not a valid canonical UUID. This
// guards the defense-in-depth re-validation at the kernel CellTxManager
// string boundary.
func TestApplyTenantScope_InvalidUUID(t *testing.T) {
	mock := &mockTx{}
	ctx := CtxWithTx(context.Background(), mock)

	tm := &TxManager{pool: nil}
	err := tm.ApplyTenantScope(ctx, "not-a-uuid")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInternal, ecErr.Kind,
		"ApplyTenantScope with invalid UUID must return KindInternal")
	// No GUC write should have occurred.
	assert.Empty(t, mock.execCalls, "no Exec must be called when UUID validation fails")
}
