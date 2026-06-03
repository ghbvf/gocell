package saga

// metrics_observer_test.go — coverage for the Coordinator-emitted observer
// metrics added in #1109: saga_tick_total / saga_drive_total /
// saga_leader_elect_skip_total, plus the classifyLeaderSkip single-source
// classification and the safeObserve panic guard.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/saga/executor"
)

// recordingObserver captures Coordinator-emitted Observer events (concurrent-safe).
// Executor-emitted methods are no-ops here — this fake exists to assert the
// coordinator-level fan-out.
type recordingObserver struct {
	mu          sync.Mutex
	ticks       []executor.TickResult
	drives      []recordedDrive
	leaderSkips []recordedSkip
}

type recordedDrive struct {
	definitionID string
	result       executor.DriveResult
}

type recordedSkip struct {
	definitionID string
	reason       executor.LeaderSkipReason
}

func (o *recordingObserver) ObserveOutcome(context.Context, idutil.SafeID, idutil.SafeID, string, string, executor.Outcome, int) {
}

func (o *recordingObserver) ObserveRetry(context.Context, idutil.SafeID, idutil.SafeID, string, string) {
}

func (o *recordingObserver) ObserveHeartbeatFailure(context.Context, idutil.SafeID, idutil.SafeID, executor.HeartbeatFailureReason) {
}

func (o *recordingObserver) ObserveTick(_ context.Context, result executor.TickResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ticks = append(o.ticks, result)
}

func (o *recordingObserver) ObserveDrive(_ context.Context, definitionID string, result executor.DriveResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.drives = append(o.drives, recordedDrive{definitionID: definitionID, result: result})
}

func (o *recordingObserver) ObserveLeaderSkip(_ context.Context, definitionID string, reason executor.LeaderSkipReason) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.leaderSkips = append(o.leaderSkips, recordedSkip{definitionID: definitionID, reason: reason})
}

func (o *recordingObserver) snapshotTicks() []executor.TickResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]executor.TickResult(nil), o.ticks...)
}

func (o *recordingObserver) snapshotDrives() []recordedDrive {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]recordedDrive(nil), o.drives...)
}

func (o *recordingObserver) snapshotSkips() []recordedSkip {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]recordedSkip(nil), o.leaderSkips...)
}

var _ executor.Observer = (*recordingObserver)(nil)

// errLocker is a distlock.Locker that fails every Acquire with a fixed error —
// used to drive each LeaderSkipReason branch of acquireLead.
type errLocker struct{ err error }

func (l errLocker) Acquire(context.Context, string, time.Duration) (*distlock.Lock, error) {
	return nil, l.err
}
func (l errLocker) Stats() distlock.Stats { return distlock.Stats{} }

// claimErrJournal wraps a journal and forces ClaimPending to fail.
type claimErrJournal struct {
	journal.Journal
	err error
}

func (j claimErrJournal) ClaimPending(context.Context, int, time.Duration) ([]journal.ClaimedInstance, idutil.SafeID, error) {
	return nil, "", j.err
}

// ---------------------------------------------------------------------------
// classifyLeaderSkip / leaderSkipLogLevel — pure classification
// ---------------------------------------------------------------------------

func TestClassifyLeaderSkip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want executor.LeaderSkipReason
	}{
		{"distlock_timeout", errcode.New(errcode.KindConflict, errcode.ErrDistlockTimeout, "lock held"), executor.LeaderSkipContended},
		{"ctx_canceled", context.Canceled, executor.LeaderSkipCtxCanceled},
		{"ctx_deadline", context.DeadlineExceeded, executor.LeaderSkipCtxCanceled},
		{"backend_error", errors.New("redis down"), executor.LeaderSkipBackendError},
	}
	for _, tc := range cases {
		if got := classifyLeaderSkip(tc.err); got != tc.want {
			t.Errorf("classifyLeaderSkip(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLeaderSkipLogLevel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason executor.LeaderSkipReason
		want   slog.Level
	}{
		{executor.LeaderSkipContended, slog.LevelDebug},
		{executor.LeaderSkipCtxCanceled, slog.LevelDebug},
		{executor.LeaderSkipBackendError, slog.LevelWarn},
	}
	for _, tc := range cases {
		if got := leaderSkipLogLevel(tc.reason); got != tc.want {
			t.Errorf("leaderSkipLogLevel(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// acquireLead emits ObserveLeaderSkip with the classified reason
// ---------------------------------------------------------------------------

func TestAcquireLead_Skip_EmitsLeaderSkipReason(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reason executor.LeaderSkipReason
	}{
		{"contended", errcode.New(errcode.KindConflict, errcode.ErrDistlockTimeout, "held"), executor.LeaderSkipContended},
		{"ctx_canceled", context.Canceled, executor.LeaderSkipCtxCanceled},
		{"backend_error", errors.New("redis down"), executor.LeaderSkipBackendError},
	}
	// def-skip is registered so labelDefinitionID passes the real ID through
	// (the unregistered → "_unregistered" sentinel path is covered separately by
	// TestAcquireLead_Skip_UnregisteredDefinition_Sentinel).
	skipDef := &ksaga.Definition{
		ID:    "def-skip",
		Steps: []ksaga.Step{{Name: "s1", Run: func(context.Context, *ksaga.Instance, []byte) ([]byte, error) { return nil, nil }}},
	}
	reg, err := ksaga.NewInMemoryRegistry(skipDef)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := newFakeClock()
			obs := &recordingObserver{}
			c, err := NewCoordinator(newMemJournal(clk), newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
				WithConfig(leaderElectCfg()), WithObserver(obs), WithLeaderElect(errLocker{err: tc.err}))
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}
			ci := journal.ClaimedInstance{
				Instance: ksaga.NewInstance(mustNewUUID(t), "def-skip", clk.Now()),
				LeaseID:  "lease-1",
			}
			_, _, lead := c.acquireLead(context.Background(), ci)
			if lead {
				t.Fatal("acquireLead returned lead=true on Acquire error")
			}
			skips := obs.snapshotSkips()
			if len(skips) != 1 {
				t.Fatalf("ObserveLeaderSkip calls = %d, want 1", len(skips))
			}
			if skips[0].reason != tc.reason {
				t.Errorf("reason = %q, want %q", skips[0].reason, tc.reason)
			}
			if skips[0].definitionID != "def-skip" {
				t.Errorf("definitionID = %q, want def-skip", skips[0].definitionID)
			}
		})
	}
}

// TestAcquireLead_Skip_UnregisteredDefinition_Sentinel covers F3: a claimed
// instance whose DefinitionID is not in the registry collapses to the
// "_unregistered" sentinel in the metric label (bounded cardinality), rather
// than leaking an unbounded attacker-influenced DefinitionID.
func TestAcquireLead_Skip_UnregisteredDefinition_Sentinel(t *testing.T) {
	clk := newFakeClock()
	obs := &recordingObserver{}
	c, err := NewCoordinator(newMemJournal(clk), newSafeFakeTxRunner(), newSafeFakeEmitter(), newRegistry(), clk,
		WithConfig(leaderElectCfg()), WithObserver(obs),
		WithLeaderElect(errLocker{err: errcode.New(errcode.KindConflict, errcode.ErrDistlockTimeout, "held")}))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	ci := journal.ClaimedInstance{
		Instance: ksaga.NewInstance(mustNewUUID(t), "totally-unregistered-def", clk.Now()),
		LeaseID:  "lease-1",
	}
	if _, _, lead := c.acquireLead(context.Background(), ci); lead {
		t.Fatal("acquireLead returned lead=true on Acquire error")
	}
	skips := obs.snapshotSkips()
	if len(skips) != 1 {
		t.Fatalf("ObserveLeaderSkip calls = %d, want 1", len(skips))
	}
	if skips[0].definitionID != unregisteredDefinitionLabel {
		t.Errorf("definitionID = %q, want %q (sentinel for unregistered def)", skips[0].definitionID, unregisteredDefinitionLabel)
	}
}

// ---------------------------------------------------------------------------
// tickOnce emits ObserveTick (empty / error / claimed) and ObserveDrive
// ---------------------------------------------------------------------------

func TestTickOnce_EmptyClaim_EmitsTickEmpty(t *testing.T) {
	clk := newFakeClock()
	obs := &recordingObserver{}
	c, err := NewCoordinator(newMemJournal(clk), newSafeFakeTxRunner(), newSafeFakeEmitter(), newRegistry(), clk,
		WithObserver(obs))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if err := c.tickOnce(context.Background()); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if ticks := obs.snapshotTicks(); len(ticks) != 1 || ticks[0] != executor.TickEmpty {
		t.Errorf("ticks = %v, want [empty]", ticks)
	}
	if len(obs.snapshotDrives()) != 0 {
		t.Error("no drive expected on empty claim")
	}
}

func TestTickOnce_ClaimError_EmitsTickError(t *testing.T) {
	clk := newFakeClock()
	obs := &recordingObserver{}
	j := claimErrJournal{
		Journal: newMemJournal(clk),
		err:     errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "journal down"),
	}
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), newRegistry(), clk,
		WithObserver(obs))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if err := c.tickOnce(context.Background()); err == nil {
		t.Fatal("tickOnce should return the ClaimPending error")
	}
	if ticks := obs.snapshotTicks(); len(ticks) != 1 || ticks[0] != executor.TickError {
		t.Errorf("ticks = %v, want [error]", ticks)
	}
}

func TestTickOnce_Claimed_EmitsTickAndDriveOK(t *testing.T) {
	const defID idutil.SafeID = "tickdrive"
	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{{
			Name: "step1",
			Run: func(context.Context, *ksaga.Instance, []byte) ([]byte, error) {
				return []byte(`{"ok":true}`), nil
			},
		}},
	}
	clk := newFakeClock()
	j := newMemJournal(clk)
	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}
	obs := &recordingObserver{}
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk, WithObserver(obs))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := c.tickOnce(context.Background()); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if ticks := obs.snapshotTicks(); len(ticks) != 1 || ticks[0] != executor.TickClaimed {
		t.Errorf("ticks = %v, want [claimed]", ticks)
	}
	drives := obs.snapshotDrives()
	if len(drives) != 1 {
		t.Fatalf("drives = %d, want 1", len(drives))
	}
	if drives[0].result != executor.DriveOK {
		t.Errorf("drive result = %q, want ok", drives[0].result)
	}
	if drives[0].definitionID != string(defID) {
		t.Errorf("drive definition_id = %q, want %q", drives[0].definitionID, defID)
	}
}

func TestTickOnce_Claimed_EmitsDriveError(t *testing.T) {
	const defID idutil.SafeID = "tickdriveerr"
	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{{
			Name: "step1",
			Run: func(context.Context, *ksaga.Instance, []byte) ([]byte, error) {
				return []byte(`{"ok":true}`), nil
			},
		}},
	}
	clk := newFakeClock()
	// failingFakeJournal fails the first Append (the step-completed event), so
	// driveOne returns a non-nil error → ObserveDrive(DriveError).
	j := newFailingFakeJournal(newMemJournal(clk))
	j.failAppendOnCall = 1
	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}
	obs := &recordingObserver{}
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk, WithObserver(obs))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := c.tickOnce(context.Background()); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if ticks := obs.snapshotTicks(); len(ticks) != 1 || ticks[0] != executor.TickClaimed {
		t.Errorf("ticks = %v, want [claimed]", ticks)
	}
	drives := obs.snapshotDrives()
	if len(drives) != 1 {
		t.Fatalf("drives = %d, want 1", len(drives))
	}
	if drives[0].result != executor.DriveError {
		t.Errorf("drive result = %q, want error", drives[0].result)
	}
}

// ---------------------------------------------------------------------------
// safeObserve isolates a panicking observer and bounds a blocking one
// ---------------------------------------------------------------------------

func TestSafeObserve_PanicIsolation(t *testing.T) {
	clk := newFakeClock()
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := NewCoordinator(newMemJournal(clk), newSafeFakeTxRunner(), newSafeFakeEmitter(), newRegistry(), clk,
		WithLogger(logger))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	// Must not propagate the panic.
	c.safeObserve(context.Background(), "ObserveTick", func() { panic("observer boom") })

	entry := sloghelper.FindLogEntry(buf.String(), "observer call panicked")
	if entry == nil {
		t.Fatalf("expected an observer-panic WARN log; logs=%s", buf.String())
	}
	if entry["method"] != "ObserveTick" {
		t.Errorf("panic log method = %v, want ObserveTick", entry["method"])
	}
	if _, ok := entry["panic"]; !ok {
		t.Errorf("expected a redacted 'panic' field in the recover log; got %v", entry)
	}
}

// TestSafeObserve_BlockingObserver_DoesNotStall covers F4 (#1109): a blocking
// out-of-tree observer must not stall the coordinator tick/drive loop. safeObserve
// runs the call on a bounded goroutine; once the clock passes
// observerCallDeadline the caller logs Warn and returns, freeing the per-instance
// distlock (release() / inflightLocks.Delete run AFTER ObserveDrive in tickOnce).
// Mirrors executor TestRunWithHeartbeat_BlockingObserver_DoesNotStall.
func TestSafeObserve_BlockingObserver_DoesNotStall(t *testing.T) {
	clk := newFakeClock()
	buf := sloghelper.NewSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := NewCoordinator(newMemJournal(clk), newSafeFakeTxRunner(), newSafeFakeEmitter(), newRegistry(), clk,
		WithLogger(logger))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release) // unblock the observer goroutine on exit (no leak across tests)

	done := make(chan struct{})
	go func() {
		c.safeObserve(context.Background(), "ObserveDrive", func() {
			close(entered)
			<-release // block well past the deadline
		})
		close(done)
	}()

	// The observer call has entered. The bounded timer is created synchronously
	// right after the goroutine spawn (no yield between `go` and NewTimerAt), so
	// it is already registered at frozenNow+deadline before this point.
	<-entered

	// Advance past the bounded deadline; the timer fires and safeObserve returns
	// without waiting for the still-blocked observer.
	clk.Advance(c.observerCallDeadline + time.Second)

	testwait.Deterministic(t, done, "safe-observe-returned")

	entry := sloghelper.FindLogEntry(buf.String(), "observer call exceeded deadline")
	if entry == nil {
		t.Fatalf("expected an exceeded-deadline WARN log; logs=%s", buf.String())
	}
	if entry["method"] != "ObserveDrive" {
		t.Errorf("deadline log method = %v, want ObserveDrive", entry["method"])
	}
}
