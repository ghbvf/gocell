package assembly_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
)

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
	assert.PanicsWithValue(t, wantNilClockPanic, func() {
		_ = assembly.New(assembly.Config{
			ID:             "nil-clock",
			DurabilityMode: cell.DurabilityDemo,
			// Clock intentionally omitted — must panic.
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
	assert.PanicsWithValue(t, wantTypedNilClockPanic, func() {
		_ = assembly.New(assembly.Config{
			ID:             "typed-nil-clock",
			DurabilityMode: cell.DurabilityDemo,
			Clock:          clk,
		})
	})
}

// TestAssemblyStart_DepsCarryClock verifies that the assembly's configured
// Clock is propagated into cell.Dependencies for every Init call.
func TestAssemblyStart_DepsCarryClock(t *testing.T) {
	t.Parallel()

	want := clock.Real()
	a := assembly.New(assembly.Config{
		ID:             "deps-clock-test",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          want,
	})
	t.Cleanup(a.Shutdown)

	c := newClockObservingCell("co")
	require.NoError(t, a.Register(c))
	require.NoError(t, a.Start(context.Background()))
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	assert.Equal(t, want, c.gotClock,
		"Init received deps.Clock = %v, want %v (same value as Config.Clock)",
		c.gotClock, want)
}

// clockObservingCell records the Clock instance it receives via Dependencies
// during Init.
type clockObservingCell struct {
	*cell.BaseCell
	gotClock clock.Clock
}

func newClockObservingCell(id string) *clockObservingCell {
	return &clockObservingCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore, ConsistencyLevel: cell.L0}),
	}
}

func (c *clockObservingCell) Init(ctx context.Context, deps cell.Dependencies) error {
	c.gotClock = deps.Clock
	return c.BaseCell.Init(ctx, deps)
}
