//go:build integration

package sagaleader

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	pgsaga "github.com/ghbvf/gocell/adapters/postgres/saga"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
	"github.com/ghbvf/gocell/runtime/saga"
	"github.com/ghbvf/gocell/runtime/saga/executor"
)

// noopEmitter is a koutbox.Emitter that discards entries — leader-elect tests
// assert on the journal + distlock, not the outbox.
type noopEmitter struct{}

func (noopEmitter) Emit(context.Context, koutbox.Entry) error { return nil }

// leaderPGCfg is the coordinator config for the two-process PG test: fast poll
// (clock-driven), small batch, long lease (no expiry within the test).
// Heartbeat lives on the Executor now (#1181) — see newLeaderPGExecutor.
func leaderPGCfg() saga.Config {
	return saga.Config{
		PollInterval:   testtime.D10ms,
		ClaimBatchSize: 4,
		LeaseDuration:  testtime.D60s,
	}
}

// newLeaderPGExecutor constructs the Executor injected into every leader-elect
// Coordinator in this suite. The heartbeat interval (20s, = LeaseDuration/3)
// mirrors the pre-#1181 leaderPGCfg.HeartbeatInterval default.
func newLeaderPGExecutor(t *testing.T, j journal.Journal, clk *clockmock.FakeClock) *executor.Executor {
	t.Helper()
	exec, err := executor.NewExecutor(j, clk,
		executor.WithHeartbeatInterval(testtime.D20s),
		executor.WithLeaseDuration(testtime.D60s),
	)
	require.NoError(t, err)
	return exec
}

// newPGStore clones one fresh migrated DB and returns a PGJournal + a TxManager
// over the SAME pool, so journal Append/MarkTerminal commit through the tx and
// are visible to journal reads. The pool is closed via t.Cleanup.
func newPGStore(t *testing.T, clk *clockmock.FakeClock) (journal.Journal, *adapterpg.TxManager) {
	t.Helper()
	dsn := sharedPG.CloneDSN(t)
	pool, err := adapterpg.NewPool(context.Background(), adapterpg.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(context.Background()) })
	j, err := pgsaga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)
	return j, adapterpg.NewTxManager(pool)
}

// startLeaderCoord builds a leader-elect coordinator over the shared journal +
// tx + the given locker, starts it in a goroutine, and registers Stop cleanup.
func startLeaderCoord(
	t *testing.T,
	j journal.Journal,
	tx *adapterpg.TxManager,
	clk *clockmock.FakeClock,
	reg ksaga.Resolver,
	locker distlock.Locker,
) {
	t.Helper()
	c, err := saga.NewCoordinator(j, tx, noopEmitter{}, reg, clk,
		saga.WithConfig(leaderPGCfg()),
		saga.WithExecutor(newLeaderPGExecutor(t, j, clk)),
		saga.WithLeaderElect(locker))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()

	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		cancel()
		t.Fatal("coordinator did not become ready")
	}

	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D5s)
		defer stopCancel()
		_ = c.Stop(stopCtx)
		select {
		case <-done:
		case <-time.After(testtime.D5s):
			t.Error("coordinator goroutine did not exit")
		}
	})
}

// TestSagaLeaderElect_TwoCoordinators_PG_ExactlyOnce drives N saga instances
// with two leader-elect coordinators sharing one PG journal and one distlock
// backend, asserting every instance is driven to terminal exactly once across
// both processes (the issue's "同 instance 只有一个进程 advance"), and that the
// distlock path is exercised.
func TestSagaLeaderElect_TwoCoordinators_PG_ExactlyOnce(t *testing.T) {
	const defID idutil.SafeID = "leaderdef"
	const numInstances = 10

	var mu sync.Mutex
	runs := make(map[idutil.SafeID]int)

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{{
			Name: "step1",
			Run: func(_ context.Context, inst *ksaga.Instance, _ []byte) ([]byte, error) {
				mu.Lock()
				runs[inst.ID]++
				mu.Unlock()
				return []byte(`{"ok":true}`), nil
			},
		}},
	}

	clk := clockmock.New(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))
	j, tx := newPGStore(t, clk)
	reg, err := ksaga.NewInMemoryRegistry(def)
	require.NoError(t, err)

	// Shared distlock backend; two Lockers = two "processes".
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker1, err := distlock.New(fd, clk)
	require.NoError(t, err)
	locker2, err := distlock.New(fd, clk)
	require.NoError(t, err)

	// Enqueue instances.
	ctx := context.Background()
	instIDs := make([]idutil.SafeID, 0, numInstances)
	for i := 0; i < numInstances; i++ {
		id := idutil.SafeID(fmt.Sprintf("le-inst-%02d", i))
		instIDs = append(instIDs, id)
		require.NoError(t, j.Enqueue(ctx, ksaga.NewInstance(id, defID, clk.Now())))
	}

	startLeaderCoord(t, j, tx, clk, reg, locker1)
	startLeaderCoord(t, j, tx, clk, reg, locker2)

	// Wait for both coordinators' tick+heartbeat tickers to register (4 total).
	testwait.External(t, "both-coordinator-tickers-registered",
		func() bool { return clk.PendingTickers() >= 4 },
		testtime.D2s, testtime.D1ms)

	// Drive: advance the clock on each poll until every instance is terminal.
	testwait.External(t, "all-instances-terminal",
		func() bool {
			clk.Advance(testtime.D10ms)
			return countTerminal(t, j, instIDs) == numInstances
		},
		testtime.D10s, testtime.D5ms)

	// Safety (execution side): each instance's Step.Run executed exactly once
	// (no split-brain side effect).
	mu.Lock()
	for _, id := range instIDs {
		require.Equalf(t, 1, runs[id], "instance %s Step.Run count", id)
	}
	mu.Unlock()

	// Safety (journal side): each instance committed exactly one StepCompleted
	// and zero StepFailed — proving neither coordinator double-committed (the
	// distlock gate + journal lease_id CAS held). This is stronger than the
	// Step.Run counter, which alone could not distinguish "only one drove" from
	// "both drove but the second's commit was CAS-fenced".
	for _, id := range instIDs {
		evs, err := j.Load(ctx, id)
		require.NoError(t, err)
		completed, failed := 0, 0
		for i := range evs {
			switch evs[i].Kind {
			case journal.KindStepCompleted:
				completed++
			case journal.KindStepFailed:
				failed++
			}
		}
		require.Equalf(t, 1, completed, "instance %s KindStepCompleted count", id)
		require.Zerof(t, failed, "instance %s KindStepFailed count", id)
	}

	// The distlock path was exercised (acquire + release on the shared backend).
	require.Positive(t, fd.Calls("SetNX"), "distlock SetNX not exercised")
	require.Positive(t, fd.Calls("Release"), "distlock Release not exercised")
}

// countTerminal returns how many of the given instances have a terminal last
// journal event.
func countTerminal(t *testing.T, j journal.Journal, ids []idutil.SafeID) int {
	t.Helper()
	n := 0
	for _, id := range ids {
		evs, err := j.Load(context.Background(), id)
		require.NoError(t, err)
		if len(evs) > 0 && evs[len(evs)-1].Kind.IsTerminal() {
			n++
		}
	}
	return n
}
