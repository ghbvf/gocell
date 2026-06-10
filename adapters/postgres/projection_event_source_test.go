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
	"github.com/ghbvf/gocell/kernel/healthz"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// compile-time interface checks mirror the production assertions so a signature drift
// surfaces in the unit build, not only at the integration call site.
var (
	_ projection.ReplaySource = (*PGProjectionEventSource)(nil)
	_ projection.Cursor       = (*PGProjectionEventSource)(nil)
	_ healthz.RepoProber      = (*PGProjectionEventSource)(nil)
)

func TestNewProjectionEventSource_NilPool(t *testing.T) {
	t.Parallel()
	src, err := NewProjectionEventSource(nil)
	require.Error(t, err)
	assert.Nil(t, src)
	assert.Equal(t, errcode.ErrValidationFailed, codeOf(t, err))
}

// newEventSourceWithTx builds an event source whose pgexec executor wraps a nil pool;
// injecting a mock tx via CtxWithTx routes every statement to the tx, so the SQL methods
// run without dereferencing the nil pool (same trick as projection_replay_source_test.go).
func newEventSourceWithTx(tx pgx.Tx) (*PGProjectionEventSource, context.Context) {
	s := &PGProjectionEventSource{db: pgexec.New(nil)}
	return s, CtxWithTx(context.Background(), tx)
}

func TestPGProjectionEventSource_Head(t *testing.T) {
	t.Parallel()
	t.Run("value", func(t *testing.T) {
		t.Parallel()
		tx := &mockProjTx{row: &mockProjRow{value: 21}}
		s, ctx := newEventSourceWithTx(tx)
		head, err := s.Head(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(21), head)
		assert.Equal(t, projectionEventHeadSQL, tx.row.sql)
	})
	t.Run("query error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("head boom")
		tx := &mockProjTx{row: &mockProjRow{scanErr: sentinel}}
		s, ctx := newEventSourceWithTx(tx)
		_, err := s.Head(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
}

func TestPGProjectionEventSource_Replay(t *testing.T) {
	t.Parallel()
	t.Run("delivers carriers then stops", func(t *testing.T) {
		t.Parallel()
		e1 := mustEntry(t)
		e2 := mustEntry(t)
		tx := &mockProjTx{rows: &mockProjRows{specs: []rowSpec{{seq: 1, entry: e1}, {seq: 2, entry: e2}}}}
		s, ctx := newEventSourceWithTx(tx)

		var gotIDs []string
		var gotSeqs []int64
		err := s.Replay(ctx, 0, func(e projection.ProjectionEvent) error {
			gotIDs = append(gotIDs, e.EventID())
			// Each delivered carrier exposes its own global_seq (the #1504 contract).
			gotSeqs = append(gotSeqs, e.(*projection.JournalEvent).GlobalSeq())
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, []string{e1.ID(), e2.ID()}, gotIDs)
		assert.Equal(t, []int64{1, 2}, gotSeqs)
		assert.Equal(t, projectionEventReplaySQL, tx.rows.sql)
		assert.Equal(t, []any{int64(0)}, tx.rows.args)
	})
	t.Run("fn error stops immediately", func(t *testing.T) {
		t.Parallel()
		tx := &mockProjTx{rows: &mockProjRows{specs: []rowSpec{{seq: 1, entry: mustEntry(t)}}}}
		s, ctx := newEventSourceWithTx(tx)
		sentinel := errors.New("apply boom")
		err := s.Replay(ctx, 0, func(projection.ProjectionEvent) error { return sentinel })
		assert.ErrorIs(t, err, sentinel)
	})
	t.Run("query error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("query boom")
		tx := &mockProjTx{queryErr: sentinel}
		s, ctx := newEventSourceWithTx(tx)
		err := s.Replay(ctx, 5, func(projection.ProjectionEvent) error { return nil })
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
	t.Run("iterate error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("iterate boom")
		tx := &mockProjTx{rows: &mockProjRows{iterErr: sentinel}}
		s, ctx := newEventSourceWithTx(tx)
		err := s.Replay(ctx, 0, func(projection.ProjectionEvent) error { return nil })
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
}

func TestScanProjectionEvent(t *testing.T) {
	t.Parallel()
	t.Run("valid row reconstructs carrier with global_seq", func(t *testing.T) {
		t.Parallel()
		e := mustEntry(t)
		carrier, err := scanProjectionEvent(&fakeReplayScanner{spec: rowSpec{seq: 9, entry: e}})
		require.NoError(t, err)
		assert.Equal(t, e.ID(), carrier.EventID())
		assert.Equal(t, int64(9), carrier.GlobalSeq())
	})
	t.Run("scan error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("scan boom")
		_, err := scanProjectionEvent(&fakeReplayScanner{scanErr: sentinel})
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
	t.Run("ToEntry error on invalid row", func(t *testing.T) {
		t.Parallel()
		// Empty ID fails Entry.Validate inside ToEntry → scanProjectionEvent wraps it.
		_, err := scanProjectionEvent(&fakeReplayScanner{spec: rowSpec{seq: 1, entry: kout.Entry{}}})
		require.Error(t, err)
	})
}

// TestPGProjectionEventSource_PositionNoDBRoundTrip is the #1504 root-fix regression: Position
// must read the carrier's own global_seq and issue NO SQL. The source is built on a nil pool
// (pgexec.New(nil)), so ANY database round-trip in Position would panic on a nil-pointer
// dereference. Position returning the carrier's seq cleanly proves the structural fix — no
// `SELECT seq WHERE id=$1` lookup that a deleted row could break (contrast the outbox-backed
// PGProjectionCursor it replaces, whose Position issues exactly that query).
func TestPGProjectionEventSource_PositionNoDBRoundTrip(t *testing.T) {
	t.Parallel()
	s := &PGProjectionEventSource{db: pgexec.New(nil)} // nil pool: any query would panic
	carrier := projection.NewJournalEvent(mustEntry(t), 42)

	pos, err := s.Position(carrier)
	require.NoError(t, err)
	assert.Equal(t, int64(42), pos, "Position must return the carrier's intrinsic global_seq with no DB lookup")
}

// TestPGProjectionEventSource_PositionRejectsForeignCarrier asserts both unresolvable branches
// return a permanent error (Cursor invariant #4), again without touching the DB (nil pool).
func TestPGProjectionEventSource_PositionRejectsForeignCarrier(t *testing.T) {
	t.Parallel()
	s := &PGProjectionEventSource{db: pgexec.New(nil)}

	t.Run("non-JournalEvent carrier", func(t *testing.T) {
		t.Parallel()
		_, err := s.Position(mustEntry(t)) // a bare outbox.Entry is not a *JournalEvent
		assertProjPermanent(t, err)
	})
	t.Run("seq-0 sentinel", func(t *testing.T) {
		t.Parallel()
		_, err := s.Position(projection.NewJournalEvent(mustEntry(t), 0))
		assertProjPermanent(t, err)
	})
}

func TestPGProjectionEventSource_RepoReady(t *testing.T) {
	t.Parallel()
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		tx := &mockExecTx{}
		s, ctx := newEventSourceWithTx(tx)
		require.NoError(t, s.RepoReady(ctx))
		assert.Equal(t, projectionJournalReadySQL, tx.execSQL)
	})
	t.Run("query error wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("ready boom")
		tx := &mockExecTx{execErr: sentinel}
		s, ctx := newEventSourceWithTx(tx)
		err := s.RepoReady(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
	})
}

func assertProjPermanent(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var permErr *kout.PermanentError
	assert.True(t, errors.As(err, &permErr),
		"an unresolvable carrier must be permanent (Cursor invariant #4), not transient")
}

// mockExecTx embeds pgx.Tx and overrides Exec to drive RepoReady deterministically.
type mockExecTx struct {
	pgx.Tx
	execSQL string
	execErr error
}

func (m *mockExecTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	m.execSQL = sql
	return pgconn.CommandTag{}, m.execErr
}
