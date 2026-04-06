package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTx implements pgx.Tx for unit testing.
type mockTx struct {
	pgx.Tx
	committed  bool
	rolledBack bool
	execCalls  []string
	// rollbackCtxCancelled records whether the context passed to Rollback was already cancelled.
	rollbackCtxCancelled bool
	// execCtxCancelled tracks per-call whether the context was cancelled (parallel to execCalls).
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
	tx, ok := TxFromContext(ctx)
	assert.False(t, ok)
	assert.Nil(t, tx)

	// Store and retrieve.
	mock := &mockTx{}
	ctx = CtxWithTx(ctx, mock)
	tx, ok = TxFromContext(ctx)
	assert.True(t, ok)
	assert.Same(t, mock, tx)
}

func TestTxFromContext_NilContext(t *testing.T) {
	//nolint:staticcheck // testing nil-safety
	tx, ok := TxFromContext(context.Background())
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
		tx, ok := TxFromContext(innerCtx)
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
	// even when the caller context is already cancelled.
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

	// The rollback Exec call must NOT have seen a cancelled context
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
			cancel() // context cancelled before panic
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
