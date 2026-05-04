package assembly

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestAssembly_StartConcurrentSnapshots_RaceDetector exercises the path that
// PR-V1-030-K01 fixes: during startInternal Phase 1 (Init loop), `a.snapshots`
// must not be written without holding `a.mu`. Concurrent calls to Snapshots()
// hold the lock and read the map; an unlocked write from Phase 1 is a fatal
// map race under Go's runtime detector and triggers reliably under `go test
// -race`.
//
// Setup: register N cells. The last one parks in Init until `initGate` closes,
// holding Phase 1 mid-flight. Reader goroutines repeatedly call Snapshots(),
// hammering the map while earlier cells' snapshots have already been recorded.
//
// Expected:
//   - Pre-fix:  fatal "concurrent map read and map write" under -race.
//   - Post-fix: passes — Phase 1 collects into a local map and assigns under
//     `a.mu` once after Init completes.
//
// CI race gate: .github/workflows/test-race.yml runs `go test -race
// ./kernel/...`, so this test is auto-covered. Without -race it may pass on
// lucky scheduling; the gate is the contract.
func TestAssembly_StartConcurrentSnapshots_RaceDetector(t *testing.T) {
	a := newTestAssembly(t, Config{
		ID:             "race-snapshots",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})

	// Pre-register 3 fast cells so Phase 1 will populate a.snapshots
	// before reaching the blocking last cell. This guarantees readers see
	// a non-empty map mid-Init when they race against the write site.
	for i := 0; i < 3; i++ {
		id := "fast-" + string(rune('a'+i))
		require.NoError(t, a.Register(cell.MustNewBaseCell(&metadata.CellMeta{
			ID: id, Type: "core", ConsistencyLevel: "L0",
		})))
	}

	// Last cell parks Init until initGate is closed.
	initGate := make(chan struct{})
	require.NoError(t, a.Register(&configMutatingCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID: "blocking", Type: "core", ConsistencyLevel: "L0",
		}),
		onInit: func(_ cell.Registry) error {
			<-initGate
			return nil
		},
	}))

	// Kick off Start in a goroutine; it will park inside Phase 1 at "blocking".
	startDone := make(chan error, 1)
	go func() {
		startDone <- a.Start(context.Background())
	}()

	// Spin up readers that bash Snapshots() while Phase 1 is mid-flight.
	const readers = 8
	const reads = 20
	var wg sync.WaitGroup
	wg.Add(readers)
	stop := make(chan struct{})
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < reads; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = a.Snapshots()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Hold readers in a hot loop briefly so Phase 1 has time to write at least
	// one cell snapshot before unblocking.
	time.Sleep(30 * time.Millisecond)
	close(initGate)

	startErr := <-startDone
	close(stop)
	wg.Wait()

	require.NoError(t, startErr, "Start must succeed once Phase 1 unblocks")

	// Cleanup: stop assembly so registered cells transition cleanly. Stop
	// failure is non-fatal for this test (the focus is Start-time race).
	_ = a.Stop(context.Background())
}

// TestAssembly_ConcurrentStartStop_RaceDetector exercises the assembly state
// machine under concurrent Start/Stop attempts plus concurrent reads of
// Snapshots() and Health(). G1-17 (review 20260504) noted this gap: state
// transitions guarded by sync.Mutex were not exercised under -race.
//
// Expected: state guards reject re-entrant Start/Stop with errors (no fatal),
// and there is no data race on a.snapshots or a.state.
//
// Pre-fix: combined with the Phase 1 race, this stress test can also surface
// the same map race when a Start collides with a concurrent Snapshots() reader.
// Post-fix: passes cleanly.
func TestAssembly_ConcurrentStartStop_RaceDetector(t *testing.T) {
	a := newTestAssembly(t, Config{
		ID:             "race-startstop",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})

	for i := 0; i < 3; i++ {
		id := "c-" + string(rune('a'+i))
		require.NoError(t, a.Register(cell.MustNewBaseCell(&metadata.CellMeta{
			ID: id, Type: "core", ConsistencyLevel: "L0",
		})))
	}

	const writers = 4
	const readers = 4
	const iterations = 25

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	// Writers: alternate Start / Stop; the state machine must reject
	// invalid transitions without data races.
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = a.Start(context.Background())
				_ = a.Stop(context.Background())
			}
		}()
	}

	// Readers: hammer Snapshots() and Health() — both must be safe under
	// any state transition.
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations*4; i++ {
				_ = a.Snapshots()
				_ = a.Health()
			}
		}()
	}

	wg.Wait()

	// Final teardown: drive to stopped state regardless of last writer.
	_ = a.Stop(context.Background())
}
