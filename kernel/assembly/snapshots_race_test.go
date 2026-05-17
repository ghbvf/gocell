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
			ID: id, Type: "core", DurabilityMode: "demo", ConsistencyLevel: "L0",
		})))
	}

	// Last cell parks Init until initGate is closed.
	initGate := make(chan struct{})
	require.NoError(t, a.Register(&configMutatingCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID: "blocking", Type: "core", DurabilityMode: "demo", ConsistencyLevel: "L0",
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
			ID: "gated", Type: "core", DurabilityMode: "demo", ConsistencyLevel: "L0",
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
				ID: id, Type: "core", DurabilityMode: "demo", ConsistencyLevel: "L0",
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
	registeredIDs := registerConcurrentRaceCells(t, a, 3)

	const writers = 4
	const readers = 4
	const iterations = 25

	// Writers alternate Start / Stop while readers hammer Snapshots() and
	// Health(); both sides must stay race-free under invalid transitions.
	result := startConcurrentStartStopRace(a, registeredIDs, writers, readers, iterations)
	result.wait()
	requireAllowedStartErrors(t, result.startErrs)
	requireNoRaceErrors(t, result.stopErrs, "Stop must either stop stateStarted or no-op outside stateStarted")
	requireNoRaceErrors(t, result.readerErrs, "")

	// Final teardown: drive to stopped state regardless of last writer.
	_ = a.Stop(context.Background())
	assert.Nil(t, a.Snapshots(), "final state must not expose snapshots")
}

type concurrentStartStopRaceResult struct {
	wg         sync.WaitGroup
	startErrs  chan error
	stopErrs   chan error
	readerErrs chan error
}

func registerConcurrentRaceCells(t *testing.T, a *CoreAssembly, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		id := "c-" + string(rune('a'+i))
		require.NoError(t, a.Register(cell.MustNewBaseCell(&metadata.CellMeta{
			ID: id, Type: "core", DurabilityMode: "demo", ConsistencyLevel: "L0",
		})))
		ids = append(ids, id)
	}
	return ids
}

func startConcurrentStartStopRace(
	a *CoreAssembly,
	registeredIDs []string,
	writers, readers, iterations int,
) *concurrentStartStopRaceResult {
	result := &concurrentStartStopRaceResult{
		startErrs:  make(chan error, writers*iterations),
		stopErrs:   make(chan error, writers*iterations),
		readerErrs: make(chan error, readers*iterations*4),
	}
	result.wg.Add(writers + readers)
	startRaceWriters(a, writers, iterations, result)
	startRaceReaders(a, registeredIDs, readers, iterations*4, result)
	return result
}

func startRaceWriters(a *CoreAssembly, writers, iterations int, result *concurrentStartStopRaceResult) {
	for range writers {
		go runRaceWriter(a, iterations, result)
	}
}

func runRaceWriter(a *CoreAssembly, iterations int, result *concurrentStartStopRaceResult) {
	defer result.wg.Done()
	for range iterations {
		result.startErrs <- a.Start(context.Background())
		result.stopErrs <- a.Stop(context.Background())
	}
}

func startRaceReaders(
	a *CoreAssembly,
	registeredIDs []string,
	readers, reads int,
	result *concurrentStartStopRaceResult,
) {
	for range readers {
		go runRaceReader(a, registeredIDs, reads, result)
	}
}

func runRaceReader(a *CoreAssembly, registeredIDs []string, reads int, result *concurrentStartStopRaceResult) {
	defer result.wg.Done()
	for range reads {
		recordSnapshotValidationError(a.Snapshots(), registeredIDs, result.readerErrs)
		recordHealthValidationError(a.Health(), registeredIDs, result.readerErrs)
	}
}

func recordSnapshotValidationError(
	snaps map[string]cell.RegistrySnapshot,
	registeredIDs []string,
	readerErrs chan<- error,
) {
	for id := range snaps {
		if !slices.Contains(registeredIDs, id) {
			readerErrs <- fmt.Errorf("Snapshots() returned unregistered cell ID %q", id)
		}
	}
}

func recordHealthValidationError(
	health map[string]cell.HealthStatus,
	registeredIDs []string,
	readerErrs chan<- error,
) {
	for _, id := range registeredIDs {
		if _, ok := health[id]; !ok {
			readerErrs <- fmt.Errorf("Health() missing registered cell ID %q", id)
		}
	}
}

func (r *concurrentStartStopRaceResult) wait() {
	r.wg.Wait()
	close(r.startErrs)
	close(r.stopErrs)
	close(r.readerErrs)
}

func requireAllowedStartErrors(t *testing.T, startErrs <-chan error) {
	t.Helper()
	for err := range startErrs {
		if err != nil {
			requireStartStateGuardError(t, err)
		}
	}
}

func requireNoRaceErrors(t *testing.T, errs <-chan error, msg string) {
	t.Helper()
	for err := range errs {
		require.NoError(t, err, msg)
	}
}

func requireStartStateGuardError(t *testing.T, err error) {
	t.Helper()
	var ec *errcode.Error
	require.Truef(t, errors.As(err, &ec), "Start error must be errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Truef(t, strings.Contains(ec.Message, "cannot start in current state"),
		"Start error must describe the guarded lifecycle state, got: %v", err)
}
