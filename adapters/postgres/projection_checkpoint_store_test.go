package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// compile-time interface check mirrors the production assertion so a signature
// drift surfaces in the unit build, not only at the integration call site.
var _ projection.CheckpointStore = (*ProjectionCheckpointStore)(nil)

func TestNewProjectionCheckpointStore_NilPool(t *testing.T) {
	t.Parallel()
	store, err := NewProjectionCheckpointStore(nil)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Equal(t, errcode.ErrValidationFailed, codeOf(t, err))
}

// codeOf extracts the errcode.Code from a wrapped *errcode.Error.
func codeOf(t *testing.T, err error) errcode.Code {
	t.Helper()
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must wrap *errcode.Error: %v", err)
	return ec.Code
}

// newStoreWithTx builds a store whose pgexec executor wraps a nil pool. Every
// SQL method routes through persistence.TxFromContext first, so injecting tx via
// CtxWithTx exercises the store logic without ever dereferencing the nil pool.
func newStoreWithTx(tx pgx.Tx) (*ProjectionCheckpointStore, context.Context) {
	s := &ProjectionCheckpointStore{db: pgexec.New(nil)}
	return s, CtxWithTx(context.Background(), tx)
}

func TestProjectionCheckpointStore_SaveOffset_UpsertSQLAndArgs(t *testing.T) {
	t.Parallel()
	tx := &mockCheckpointTx{}
	s, ctx := newStoreWithTx(tx)

	err := s.SaveOffset(ctx, "ordercell", "ordersummary", 42)
	require.NoError(t, err)

	require.Len(t, tx.execCalls, 1)
	assert.Equal(t, upsertCheckpointSQL, tx.execCalls[0].sql)
	assert.Equal(t, []any{"ordercell", "ordersummary", int64(42)}, tx.execCalls[0].args)
	// owner column must NOT appear in SaveOffset (the base CheckpointStore
	// contract). The OwnerCheckpointStore.AdvanceIfOwner path writes owner via
	// insertCheckpointWithOwnerSQL / updateCheckpointWithOwnerSQL instead.
	assert.NotContains(t, tx.execCalls[0].sql, "owner")
}

func TestProjectionCheckpointStore_SaveOffset_ExecError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("exec boom")
	tx := &mockCheckpointTx{execErr: sentinel}
	s, ctx := newStoreWithTx(tx)

	err := s.SaveOffset(ctx, "ordercell", "ordersummary", 7)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
}

func TestProjectionCheckpointStore_LoadOffset_Value(t *testing.T) {
	t.Parallel()
	tx := &mockCheckpointTx{row: &mockCheckpointRow{value: 99}}
	s, ctx := newStoreWithTx(tx)

	got, err := s.LoadOffset(ctx, "ordercell", "ordersummary")
	require.NoError(t, err)
	assert.Equal(t, int64(99), got)
	assert.Equal(t, selectCheckpointSQL, tx.row.sql)
	assert.Equal(t, []any{"ordercell", "ordersummary"}, tx.row.args)
}

func TestProjectionCheckpointStore_LoadOffset_ColdStart(t *testing.T) {
	t.Parallel()
	tx := &mockCheckpointTx{row: &mockCheckpointRow{scanErr: pgx.ErrNoRows}}
	s, ctx := newStoreWithTx(tx)

	got, err := s.LoadOffset(ctx, "ordercell", "ordersummary")
	require.NoError(t, err, "cold start (no row) must return (0, nil), not an error")
	assert.Equal(t, int64(0), got)
}

func TestProjectionCheckpointStore_LoadOffset_ScanError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("scan boom")
	tx := &mockCheckpointTx{row: &mockCheckpointRow{scanErr: sentinel}}
	s, ctx := newStoreWithTx(tx)

	_, err := s.LoadOffset(ctx, "ordercell", "ordersummary")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
}

// ─── mocks ──────────────────────────────────────────────────────────────────

// mockCheckpointTx embeds pgx.Tx to satisfy the full interface; only Exec and
// QueryRow are overridden (the two methods the store calls). cpExecCall avoids a
// redeclaration of execCall from outbox_writer_test.go (same package).
type mockCheckpointTx struct {
	pgx.Tx
	execCalls []cpExecCall
	execErr   error
	row       *mockCheckpointRow
}

type cpExecCall struct {
	sql  string
	args []any
}

func (m *mockCheckpointTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.execCalls = append(m.execCalls, cpExecCall{sql: sql, args: args})
	if m.execErr != nil {
		return pgconn.NewCommandTag(""), m.execErr
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (m *mockCheckpointTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if m.row == nil {
		// Safe fallback so a test that exercises Exec-only (m.row unset) but is
		// later extended to also call QueryRow gets a deterministic cold-start
		// row instead of a nil-pointer panic on Scan.
		return &mockCheckpointRow{scanErr: pgx.ErrNoRows}
	}
	m.row.sql = sql
	m.row.args = args
	return m.row
}

// mockCheckpointRow implements pgx.Row. Scan writes value into the first *int64
// destination, or returns scanErr (e.g. pgx.ErrNoRows for cold start).
type mockCheckpointRow struct {
	sql     string
	args    []any
	value   int64
	scanErr error
}

func (r *mockCheckpointRow) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	if len(dest) > 0 {
		if p, ok := dest[0].(*int64); ok {
			*p = r.value
		}
	}
	return nil
}
