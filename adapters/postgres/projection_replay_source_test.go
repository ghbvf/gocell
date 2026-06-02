package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// compile-time interface checks mirror the production assertions so a signature
// drift surfaces in the unit build, not only at the integration call site.
var (
	_ projection.ReplaySource = (*PGProjectionReplaySource)(nil)
	_ projection.Cursor       = (*PGProjectionCursor)(nil)
)

func TestNewProjectionReplaySource_NilPool(t *testing.T) {
	t.Parallel()
	src, err := NewProjectionReplaySource(nil)
	require.Error(t, err)
	assert.Nil(t, src)
	assert.Equal(t, errcode.ErrValidationFailed, codeOf(t, err))
}

func TestNewProjectionCursor_NilSource(t *testing.T) {
	t.Parallel()
	cur, err := NewProjectionCursor(nil)
	require.Error(t, err)
	assert.Nil(t, cur)
	assert.Equal(t, errcode.ErrValidationFailed, codeOf(t, err))
}

// newReplaySourceWithTx builds a replay source whose pgexec executor wraps a nil
// pool; injecting a mock tx via CtxWithTx routes every statement to the tx, so
// the SQL methods run without ever dereferencing the nil pool (same trick as
// projection_checkpoint_store_test.go).
func newReplaySourceWithTx(tx pgx.Tx) (*PGProjectionReplaySource, context.Context) {
	s := &PGProjectionReplaySource{db: pgexec.New(nil)}
	return s, CtxWithTx(context.Background(), tx)
}

func TestPGProjectionReplaySource_Head(t *testing.T) {
	t.Parallel()
	t.Run("value", func(t *testing.T) {
		t.Parallel()
		tx := &mockProjTx{row: &mockProjRow{value: 17}}
		s, ctx := newReplaySourceWithTx(tx)
		head, err := s.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(17), head)
		assert.Equal(t, replayHeadSQL, tx.row.sql)
	})
	t.Run("query error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("head boom")
		tx := &mockProjTx{row: &mockProjRow{scanErr: sentinel}}
		s, ctx := newReplaySourceWithTx(tx)
		_, err := s.Head(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
}

func TestPGProjectionReplaySource_position(t *testing.T) {
	t.Parallel()
	t.Run("value", func(t *testing.T) {
		t.Parallel()
		tx := &mockProjTx{row: &mockProjRow{value: 42}}
		s, ctx := newReplaySourceWithTx(tx)
		pos, err := s.position(ctx, "evt-1")
		require.NoError(t, err)
		assert.Equal(t, int64(42), pos)
		assert.Equal(t, cursorPositionSQL, tx.row.sql)
		assert.Equal(t, []any{"evt-1"}, tx.row.args)
	})
	t.Run("ErrNoRows is permanent", func(t *testing.T) {
		t.Parallel()
		tx := &mockProjTx{row: &mockProjRow{scanErr: pgx.ErrNoRows}}
		s, ctx := newReplaySourceWithTx(tx)
		_, err := s.position(ctx, "missing")
		require.Error(t, err)
		var permErr *kout.PermanentError
		assert.True(t, errors.As(err, &permErr),
			"an entry absent from the journal must be permanent, not transient")
	})
	t.Run("query error is transient (wrapped, not permanent)", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("position boom")
		tx := &mockProjTx{row: &mockProjRow{scanErr: sentinel}}
		s, ctx := newReplaySourceWithTx(tx)
		_, err := s.position(ctx, "evt-1")
		require.Error(t, err)
		var permErr *kout.PermanentError
		assert.False(t, errors.As(err, &permErr), "a genuine query failure must stay transient")
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
}

func TestPGProjectionReplaySource_Replay(t *testing.T) {
	t.Parallel()
	t.Run("delivers rows then stops", func(t *testing.T) {
		t.Parallel()
		e1 := mustEntry(t)
		e2 := mustEntry(t)
		tx := &mockProjTx{rows: &mockProjRows{specs: []rowSpec{{seq: 1, entry: e1}, {seq: 2, entry: e2}}}}
		s, ctx := newReplaySourceWithTx(tx)

		var got []string
		err := s.Replay(ctx, 0, func(e kout.Entry) error {
			got = append(got, e.ID())
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, []string{e1.ID(), e2.ID()}, got)
		assert.Equal(t, replayScanSQL, tx.rows.sql)
		assert.Equal(t, []any{int64(0)}, tx.rows.args)
	})
	t.Run("fn error stops immediately", func(t *testing.T) {
		t.Parallel()
		e1 := mustEntry(t)
		tx := &mockProjTx{rows: &mockProjRows{specs: []rowSpec{{seq: 1, entry: e1}}}}
		s, ctx := newReplaySourceWithTx(tx)
		sentinel := errors.New("apply boom")
		err := s.Replay(ctx, 0, func(kout.Entry) error { return sentinel })
		assert.ErrorIs(t, err, sentinel)
	})
	t.Run("query error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("query boom")
		tx := &mockProjTx{queryErr: sentinel}
		s, ctx := newReplaySourceWithTx(tx)
		err := s.Replay(ctx, 5, func(kout.Entry) error { return nil })
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
	t.Run("iterate error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("iterate boom")
		tx := &mockProjTx{rows: &mockProjRows{iterErr: sentinel}}
		s, ctx := newReplaySourceWithTx(tx)
		err := s.Replay(ctx, 0, func(kout.Entry) error { return nil })
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
}

func TestScanReplayEntry(t *testing.T) {
	t.Parallel()
	t.Run("valid row reconstructs entry", func(t *testing.T) {
		t.Parallel()
		e := mustEntry(t)
		got, err := scanReplayEntry(&fakeReplayScanner{spec: rowSpec{seq: 9, entry: e}})
		require.NoError(t, err)
		assert.Equal(t, e.ID(), got.ID())
		assert.Equal(t, e.EventType(), got.EventType())
	})
	t.Run("scan error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("scan boom")
		_, err := scanReplayEntry(&fakeReplayScanner{scanErr: sentinel})
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
	t.Run("ToEntry error on invalid row", func(t *testing.T) {
		t.Parallel()
		// Empty ID fails Entry.Validate inside ToEntry → scanReplayEntry wraps it.
		_, err := scanReplayEntry(&fakeReplayScanner{spec: rowSpec{seq: 1, entry: kout.Entry{}}})
		require.Error(t, err)
	})
}

// ─── mocks ──────────────────────────────────────────────────────────────────

func mustEntry(t *testing.T) kout.Entry {
	t.Helper()
	e, err := kout.NewEntry(clockmock.New(time.Now()), context.Background(), "ordersummary.v1", []byte(`{}`))
	require.NoError(t, err)
	return e
}

// rowSpec carries the seq + the entry whose getters fill a replay-scan row. An
// empty kout.Entry{} models a corrupt row (ID == "" → ToEntry fails).
type rowSpec struct {
	seq   int64
	entry kout.Entry
}

// fillReplayDest writes spec into the 12 scanReplayEntry destinations, mirroring
// the replayScanSQL column order. observability/principal/metadata JSON are left
// nil so applyEntryJSONB skips them (zero values), matching a freshly-created
// entry's empty identity/metadata.
func fillReplayDest(dest []any, spec rowSpec) {
	e := spec.entry
	*(dest[0].(*int64)) = spec.seq
	*(dest[1].(*string)) = e.ID()
	*(dest[2].(*string)) = e.AggregateID()
	*(dest[3].(*string)) = e.AggregateType()
	*(dest[4].(*string)) = e.EventType()
	*(dest[5].(*string)) = e.Topic()
	*(dest[6].(*[]byte)) = e.Payload()
	*(dest[7].(*[]byte)) = nil
	*(dest[8].(*time.Time)) = e.CreatedAt()
	*(dest[9].(*[]byte)) = nil
	*(dest[10].(*[]byte)) = nil
	*(dest[11].(*time.Time)) = e.OccurredAt()
}

// fakeReplayScanner is a RowScanner for scanReplayEntry unit tests.
type fakeReplayScanner struct {
	spec    rowSpec
	scanErr error
}

func (f *fakeReplayScanner) Scan(dest ...any) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	fillReplayDest(dest, f.spec)
	return nil
}

// mockProjTx embeds pgx.Tx to satisfy the full interface; only Query and QueryRow
// are overridden (the methods the replay source calls).
type mockProjTx struct {
	pgx.Tx
	row      *mockProjRow
	rows     *mockProjRows
	queryErr error
}

func (m *mockProjTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if m.row == nil {
		return &mockProjRow{scanErr: pgx.ErrNoRows}
	}
	m.row.sql = sql
	m.row.args = args
	return m.row
}

func (m *mockProjTx) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	m.rows.sql = sql
	m.rows.args = args
	return m.rows, nil
}

// mockProjRow implements pgx.Row, writing value into the first *int64 dest (the
// seq / head scalar), or returning scanErr.
type mockProjRow struct {
	sql     string
	args    []any
	value   int64
	scanErr error
}

func (r *mockProjRow) Scan(dest ...any) error {
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

// mockProjRows embeds pgx.Rows; Next/Scan/Err/Close are overridden to drive the
// Replay loop deterministically.
type mockProjRows struct {
	pgx.Rows
	specs   []rowSpec
	idx     int
	sql     string
	args    []any
	scanErr error
	iterErr error
}

func (m *mockProjRows) Next() bool {
	if m.idx >= len(m.specs) {
		return false
	}
	m.idx++
	return true
}

func (m *mockProjRows) Scan(dest ...any) error {
	if m.scanErr != nil {
		return m.scanErr
	}
	fillReplayDest(dest, m.specs[m.idx-1])
	return nil
}

func (m *mockProjRows) Err() error { return m.iterErr }
func (m *mockProjRows) Close()     {}
