package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// compile-time interface checks mirror the production assertions so a signature drift
// surfaces in the unit build, not only at the integration call site.
var (
	_ projection.ReplaySource = (*PGProjectionEventSource)(nil)
	_ projection.LiveCursor   = (*PGProjectionEventSource)(nil)
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
// must read the carrier's own global_seq and issue NO SQL. The source's executor wraps a nil
// pool (pgexec.New(nil)) and Position is called with no ambient tx, so ANY database round-trip
// would dereference the nil pool and panic. Position returning the carrier's seq cleanly proves
// the structural fix — no `SELECT seq WHERE id=$1` lookup that a deleted row could break
// (contrast the outbox-backed cursor it replaced, whose Position issued exactly that query;
// here only the live-path ResolveCarrier does an id lookup, against the never-cleaned journal).
// pgexec.PGExecutor is a sealed interface, so a fail-on-call spy cannot be
// implemented out-of-package; the nil-pool executor is the sanctioned no-DB test seam (the same
// one newEventSourceWithTx uses).
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

// TestPGProjectionEventSource_ResolveCarrier covers the live-carrier resolver branches
// without a real DB (the mock-tx seam): an already-positioned carrier is returned
// idempotently with NO query; a bare entry resolves via the id→global_seq lookup; an
// ErrNoRows miss is PERMANENT (the never-cleaned journal makes a miss a true invariant
// violation); any other query failure is transient (wrapped ErrAdapterPGQuery, requeued).
func TestPGProjectionEventSource_ResolveCarrier(t *testing.T) {
	t.Parallel()

	t.Run("already-positioned carrier idempotent, no query", func(t *testing.T) {
		t.Parallel()
		s := &PGProjectionEventSource{db: pgexec.New(nil)} // nil pool: a query would panic
		carrier := projection.NewJournalEvent(mustEntry(t), 42)
		resolved, err := s.ResolveCarrier(context.Background(), carrier)
		require.NoError(t, err)
		assert.Equal(t, projection.ProjectionEvent(carrier), resolved, "a *JournalEvent must be returned unchanged")
	})
	t.Run("bare entry resolves via id lookup", func(t *testing.T) {
		t.Parallel()
		bare := mustEntry(t)
		tx := &mockProjTx{row: &mockProjRow{value: 7}}
		s, ctx := newEventSourceWithTx(tx)
		resolved, err := s.ResolveCarrier(ctx, bare)
		require.NoError(t, err)
		assert.Equal(t, projectionEventPositionByIDSQL, tx.row.sql)
		assert.Equal(t, []any{bare.ID()}, tx.row.args, "lookup must be keyed by the entry's id")
		assert.Equal(t, int64(7), resolved.(*projection.JournalEvent).GlobalSeq())
	})
	t.Run("ErrNoRows is permanent", func(t *testing.T) {
		t.Parallel()
		tx := &mockProjTx{} // nil row → QueryRow returns scanErr pgx.ErrNoRows
		s, ctx := newEventSourceWithTx(tx)
		_, err := s.ResolveCarrier(ctx, mustEntry(t))
		assertProjPermanent(t, err)
	})
	t.Run("query failure is transient", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("resolve boom")
		tx := &mockProjTx{row: &mockProjRow{scanErr: sentinel}}
		s, ctx := newEventSourceWithTx(tx)
		_, err := s.ResolveCarrier(ctx, mustEntry(t))
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.Equal(t, ErrAdapterPGQuery, codeOf(t, err))
		var permErr *kout.PermanentError
		assert.False(t, errors.As(err, &permErr), "a query failure must be transient (requeue), not permanent")
	})
}

// TestPGProjectionEventSource_ResolveCarrier_BoundedLookup verifies the live-carrier
// id→global_seq lookup is bounded (the retired cursorPositionTimeout, restored): a caller
// ctx without a deadline still yields a ~projectionEventLookupTimeout-bounded lookup ctx, and a
// shorter caller deadline wins (context.WithTimeout semantics). This is the regression guard for
// "a stalled DB hangs the projection worker indefinitely".
func TestPGProjectionEventSource_ResolveCarrier_BoundedLookup(t *testing.T) {
	t.Parallel()
	t.Run("no caller deadline derives a bounded lookup ctx", func(t *testing.T) {
		t.Parallel()
		tx := &mockProjTx{row: &mockProjRow{value: 3}}
		s, ctx := newEventSourceWithTx(tx) // ctx has no deadline (Background-derived)
		_, err := s.ResolveCarrier(ctx, mustEntry(t))
		require.NoError(t, err)
		dl, ok := tx.lastRowCtx.Deadline()
		require.True(t, ok, "lookup ctx must carry a deadline even when the caller has none")
		assert.InDelta(t, projectionEventLookupTimeout.Seconds(), time.Until(dl).Seconds(), 1.0,
			"bounded lookup deadline ≈ projectionEventLookupTimeout")
	})
	t.Run("shorter caller deadline wins", func(t *testing.T) {
		t.Parallel()
		tx := &mockProjTx{row: &mockProjRow{value: 3}}
		s, base := newEventSourceWithTx(tx)
		callerCtx, cancel := context.WithTimeout(base, 500*time.Millisecond)
		defer cancel()
		_, err := s.ResolveCarrier(callerCtx, mustEntry(t))
		require.NoError(t, err)
		dl, ok := tx.lastRowCtx.Deadline()
		require.True(t, ok)
		assert.Less(t, time.Until(dl), projectionEventLookupTimeout,
			"a caller deadline shorter than projectionEventLookupTimeout must win")
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
