package assembly_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Ensure time is used by nilClockCarrier methods below.
var _ = time.Time{}

// nilClockCarrier is the typed-nil carrier we feed to assembly.New to verify
// the typed-nil rejection path. It satisfies clock.Clock structurally, but
// every method panics — so if assembly.New ever lets a typed-nil value
// through, the panic surfaces inside hookDispatcher (or first Init) instead
// of at construction, which is exactly what the gate must prevent.
type nilClockCarrier struct{}

func (*nilClockCarrier) Now() time.Time                          { panic("must not be called") }
func (*nilClockCarrier) Since(time.Time) time.Duration           { panic("must not be called") }
func (*nilClockCarrier) Until(time.Time) time.Duration           { panic("must not be called") }
func (*nilClockCarrier) NewTimerAt(time.Time) clock.Timer        { panic("must not be called") }
func (*nilClockCarrier) NewTicker(time.Duration) clock.Ticker    { panic("must not be called") }
func (*nilClockCarrier) AfterFunc(time.Time, func()) clock.Timer { panic("must not be called") }
func (*nilClockCarrier) Sleep(context.Context, time.Time) error  { panic("must not be called") }

const (
	wantNilClockPanic = "assembly.New: clock.Clock is required (nil rejected); " +
		"pass clock.Real() at the composition root or clockmock.New(...) in tests"
	wantTypedNilClockPanic = "assembly.New: clock.Clock is required (typed-nil rejected); " +
		"pass clock.Real() at the composition root or clockmock.New(...) in tests"
)

// TestAssemblyNew_RejectsNilClock verifies assembly.New panics on a literal
// nil clock — the most common misconfiguration where the caller forgets to
// pass Clock at all.
func TestAssemblyNew_RejectsNilClock(t *testing.T) {
	t.Parallel()
	assertPanicsWithAssertionMessage(t, wantNilClockPanic, func() {
		_ = assembly.New(nil, assembly.Config{
			ID:             "nil-clock",
			DurabilityMode: outbox.DurabilityDemo,
		})
	})
}

// TestAssemblyNew_RejectsTypedNilClock verifies that a typed-nil pointer
// wrapped in a clock.Clock interface is also rejected at construction time,
// not at first method call. This is the Go-interface footgun the gate must
// catch: a clock.Clock value whose underlying type is *nilClockCarrier with
// a nil pointer satisfies the interface (clk != nil at the language level)
// but panics on any method invocation.
func TestAssemblyNew_RejectsTypedNilClock(t *testing.T) {
	t.Parallel()
	var p *nilClockCarrier // typed-nil pointer
	var clk clock.Clock = p
	assertPanicsWithAssertionMessage(t, wantTypedNilClockPanic, func() {
		_ = assembly.New(clk, assembly.Config{
			ID:             "typed-nil-clock",
			DurabilityMode: outbox.DurabilityDemo,
		})
	})
}

// TestAssemblyStart_SnapshotsPopulatedAfterStart verifies that after a
// successful Start, Snapshots() returns a non-nil map with one entry per
// registered cell.
func TestAssemblyStart_SnapshotsPopulatedAfterStart(t *testing.T) {
	t.Parallel()

	a := assembly.New(clock.Real(), assembly.Config{
		ID:             "snapshots-test",
		DurabilityMode: outbox.DurabilityDemo,
	})
	t.Cleanup(a.Shutdown)

	c1 := &metadata.CellMeta{ID: metadatatest.NewCellID("c1"), Type: "core", ConsistencyLevel: "L0"}
	c2 := &metadata.CellMeta{ID: metadatatest.NewCellID("c2"), Type: "core", ConsistencyLevel: "L0"}
	require.NoError(t, a.Register(cell.MustNewBaseCell(c1)))
	require.NoError(t, a.Register(cell.MustNewBaseCell(c2)))
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	snaps := a.Snapshots()
	require.NotNil(t, snaps, "Snapshots() must be non-nil after Start")
	assert.Len(t, snaps, 2, "one snapshot per registered cell")
	_, hasC1 := snaps[metadatatest.NewCellID("c1")]
	_, hasC2 := snaps[metadatatest.NewCellID("c2")]
	assert.True(t, hasC1, "snapshot for c1 must exist")
	assert.True(t, hasC2, "snapshot for c2 must exist")
}

// TestAssemblyStart_SnapshotsCopy verifies that mutations to the returned
// map do not affect the assembly's internal snapshot state.
func TestAssemblyStart_SnapshotsCopy(t *testing.T) {
	t.Parallel()

	a := assembly.New(clock.Real(), assembly.Config{
		ID:             "snapshots-copy-test",
		DurabilityMode: outbox.DurabilityDemo,
	})
	t.Cleanup(a.Shutdown)

	c1 := &metadata.CellMeta{ID: metadatatest.NewCellID("c1"), Type: "core", ConsistencyLevel: "L0"}
	require.NoError(t, a.Register(cell.MustNewBaseCell(c1)))
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	snaps := a.Snapshots()
	require.NotNil(t, snaps)

	// Mutate the returned copy — must not affect a second call.
	delete(snaps, metadatatest.NewCellID("c1"))

	snaps2 := a.Snapshots()
	require.NotNil(t, snaps2)
	_, hasC1 := snaps2[metadatatest.NewCellID("c1")]
	assert.True(t, hasC1, "deleting from first copy must not affect internal state")
}

// assertPanicsWithAssertionMessage verifies that fn panics with an *errcode.Error
// whose Message field equals wantMsg. Used to test Must* wrappers that panic
// with errcode.Assertion(...) instead of bare strings.
func assertPanicsWithAssertionMessage(t *testing.T, wantMsg string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("expected panic but did not panic")
			return
		}
		e, ok := r.(*errcode.Error)
		if !ok {
			t.Errorf("panic value is %T, want *errcode.Error; value: %v", r, r)
			return
		}
		if e.Message != wantMsg {
			t.Errorf("panic message = %q, want %q", e.Message, wantMsg)
		}
	}()
	fn()
}
