package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
)

// Shared unit-test mocks for the PG projection journal source. These were the
// no-DB test seam co-located with the (deleted) outbox-backed replay source; they
// now live here, the sole remaining consumer (projection_event_source_test.go).
// pgexec.PGExecutor is a sealed interface, so a fail-on-call spy cannot be built
// out-of-package — these pgx.Tx fakes (injected via the ambient tx) are the
// sanctioned no-DB seam.

func mustEntry(t *testing.T) kout.Entry {
	t.Helper()
	e, err := kout.NewEntry(clockmock.New(time.Now()), context.Background(), "ordersummary.v1", []byte(`{}`))
	require.NoError(t, err)
	return e
}

// rowSpec carries the seq + the entry whose getters fill a projection-event-scan
// row. An empty kout.Entry{} models a corrupt row (ID == "" → ToEntry fails).
type rowSpec struct {
	seq   int64
	entry kout.Entry
}

// fillReplayDest writes spec into the 12 scanProjectionEvent destinations, mirroring
// the projectionEventReplaySQL column order. observability/principal/metadata JSON
// are left nil so applyEntryJSONB skips them (zero values), matching a freshly
// created entry's empty identity/metadata.
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

// fakeReplayScanner is a RowScanner for scanProjectionEvent unit tests.
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
// are overridden (the methods the journal source calls).
type mockProjTx struct {
	pgx.Tx
	row        *mockProjRow
	rows       *mockProjRows
	queryErr   error
	lastRowCtx context.Context // ctx of the most recent QueryRow (for deadline assertions)
}

func (m *mockProjTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	m.lastRowCtx = ctx
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
