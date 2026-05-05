package assembly

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestAssembly_StartConcurrentSnapshots_VisibilityDuringStart verifies the
// public Snapshots() contract while Start is still in Phase 1: callers must
// see nil until the assembly reaches stateStarted, even though Init has
// already recorded local registry declarations.
func TestAssembly_StartConcurrentSnapshots_VisibilityDuringStart(t *testing.T) {
	a := newTestAssembly(t, Config{
		ID:             "race-snapshots",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})

	// Pre-register 3 fast cells so Phase 1 will populate localSnaps before
	// reaching the blocking last cell. This creates the same writer pressure
	// as the original bug without making snapshots visible before stateStarted.
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
	// Each reader signals on `ready` once it has called Snapshots() at least
	// once; the main goroutine waits for all readers to confirm before
	// unblocking Phase 1. This guarantees the race window deterministically,
	// independent of CI runner scheduling latency (no timing-based sleep).
	const readers = 8
	const reads = 20
	ready := make(chan struct{}, readers)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(readers)
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			firstDone := false
			for i := 0; i < reads; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = a.Snapshots()
				if !firstDone {
					ready <- struct{}{}
					firstDone = true
				}
				time.Sleep(testtime.D1ms) //archtest:allow:test-sleep yield between Snapshots() reads to widen the race window vs. Phase 1 writer
			}
		}()
	}

	// Wait until every reader has invoked Snapshots() at least once. After
	// this point Phase 1 has either already raced (under -race) or is poised
	// to race the moment cell.Init returns and the original buggy code path
	// would have written a.snapshots without the lock.
	for r := 0; r < readers; r++ {
		<-ready
	}
	assert.Nil(t, a.Snapshots(), "Snapshots() must stay nil while Start is blocked in Phase 1")
	close(initGate)

	startErr := <-startDone
	close(stop)
	wg.Wait()

	require.NoError(t, startErr, "Start must succeed once Phase 1 unblocks")

	// Cleanup: stop assembly so registered cells transition cleanly. Stop
	// failure is non-fatal for this test (the focus is Start-time race).
	_ = a.Stop(context.Background())
}

// TestAssembly_StartInternalSnapshotsMap_RaceDetector is the runtime race
// guard for the original PR-V1-030-K01 failure mode. It intentionally reads
// a.snapshots under a.mu from the same package while Start is in Phase 1.
// A regression that writes a.snapshots from the Init loop without a.mu races
// this reader under `go test -race`.
func TestAssembly_StartInternalSnapshotsMap_RaceDetector(t *testing.T) {
	a := newTestAssembly(t, Config{
		ID:             "race-snapshots-internal",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})

	initGate := make(chan struct{})
	var releaseOnce sync.Once
	releaseInit := func() { releaseOnce.Do(func() { close(initGate) }) }
	t.Cleanup(releaseInit)

	enteredInit := registerGatedInitCell(t, a, initGate)
	registerSlowInitCells(t, a, 16)
	startDone := startAssemblyAsync(a)
	<-enteredInit

	ready, wg := startInternalSnapshotReaders(a, 8, 100)
	waitForReaders(ready, 8)
	releaseInit()

	require.NoError(t, <-startDone)
	wg.Wait()
	_ = a.Stop(context.Background())
}

func registerGatedInitCell(t *testing.T, a *CoreAssembly, initGate <-chan struct{}) <-chan struct{} {
	t.Helper()
	enteredInit := make(chan struct{})
	require.NoError(t, a.Register(&configMutatingCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID: "gated", Type: "core", ConsistencyLevel: "L0",
		}),
		onInit: func(_ cell.Registry) error {
			close(enteredInit)
			<-initGate
			return nil
		},
	}))
	return enteredInit
}

func registerSlowInitCells(t *testing.T, a *CoreAssembly, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("slow-%02d", i)
		require.NoError(t, a.Register(&configMutatingCell{
			BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
				ID: id, Type: "core", ConsistencyLevel: "L0",
			}),
			onInit: func(_ cell.Registry) error {
				time.Sleep(testtime.D1ms) //archtest:allow:test-sleep yield between Init completions to widen the race window against internal readers
				return nil
			},
		}))
	}
}

func startAssemblyAsync(a *CoreAssembly) <-chan error {
	startDone := make(chan error, 1)
	go func() {
		startDone <- a.Start(context.Background())
	}()
	return startDone
}

func startInternalSnapshotReaders(a *CoreAssembly, readers, reads int) (<-chan struct{}, *sync.WaitGroup) {
	ready := make(chan struct{}, readers)
	var wg sync.WaitGroup
	wg.Add(readers)
	for range readers {
		go runInternalSnapshotReader(a, reads, ready, &wg)
	}
	return ready, &wg
}

func runInternalSnapshotReader(a *CoreAssembly, reads int, ready chan<- struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	for i := 0; i < reads; i++ {
		readInternalSnapshots(a)
		if i == 0 {
			ready <- struct{}{}
		}
		time.Sleep(testtime.D1ms) //archtest:allow:test-sleep keep internal readers active while Phase 1 advances
	}
}

func readInternalSnapshots(a *CoreAssembly) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id := range a.snapshots {
		_ = id
	}
}

func waitForReaders(ready <-chan struct{}, readers int) {
	for range readers {
		<-ready
	}
}

// TestAssembly_ConcurrentStartStop_RaceDetector exercises the assembly state
// machine under concurrent Start/Stop attempts plus concurrent reads of
// Snapshots() and Health(). G1-17 (review 20260504) noted this gap: state
// transitions guarded by sync.Mutex were not exercised under -race.
//
// Expected: state guards reject re-entrant Start attempts with validation
// errors, Stop remains a nil no-op outside stateStarted, and there is no data
// race on a.snapshots or a.state.
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
	registeredIDs := []string{"c-a", "c-b", "c-c"}

	var wg sync.WaitGroup
	wg.Add(writers + readers)
	startErrs := make(chan error, writers*iterations)
	stopErrs := make(chan error, writers*iterations)
	readerErrs := make(chan error, readers*iterations*4)

	// Writers: alternate Start / Stop; the state machine must reject
	// invalid transitions without data races.
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				startErrs <- a.Start(context.Background())
				stopErrs <- a.Stop(context.Background())
			}
		}()
	}

	// Readers: hammer Snapshots() and Health() — both must be safe under
	// any state transition.
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations*4; i++ {
				snaps := a.Snapshots()
				for id := range snaps {
					if !slices.Contains(registeredIDs, id) {
						readerErrs <- fmt.Errorf("Snapshots() returned unregistered cell ID %q", id)
					}
				}
				health := a.Health()
				for _, id := range registeredIDs {
					if _, ok := health[id]; !ok {
						readerErrs <- fmt.Errorf("Health() missing registered cell ID %q", id)
					}
				}
			}
		}()
	}

	wg.Wait()
	close(startErrs)
	close(stopErrs)
	close(readerErrs)

	for err := range startErrs {
		if err == nil {
			continue
		}
		requireStartStateGuardError(t, err)
	}
	for err := range stopErrs {
		require.NoError(t, err, "Stop must either stop stateStarted or no-op outside stateStarted")
	}
	for err := range readerErrs {
		require.NoError(t, err)
	}

	// Final teardown: drive to stopped state regardless of last writer.
	_ = a.Stop(context.Background())
	assert.Nil(t, a.Snapshots(), "final state must not expose snapshots")
}

func requireStartStateGuardError(t *testing.T, err error) {
	t.Helper()
	var ec *errcode.Error
	require.Truef(t, errors.As(err, &ec), "Start error must be errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Truef(t, strings.Contains(err.Error(), "cannot start in state"),
		"Start error must describe the guarded lifecycle state, got: %v", err)
}
