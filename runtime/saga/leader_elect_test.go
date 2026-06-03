package saga

// leader_elect_test.go — white-box (package saga) unit tests for the distlock
// leader-elect gate added in PR-05 (#964). Runs in the default suite using
// memjournal + a shared locktest.FakeDriver (real key-level mutual exclusion)
// + FakeClock — fully deterministic, no Docker. The two-process PG integration
// test lives in tests/integration/sagaleader (build tag integration).
//
// TestMain (goleak) is shared with integration_test.go (same package).

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// leaderElectCfg is a fast-tick config shared by the leader-elect tests.
func leaderElectCfg() Config {
	return Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}
}

// oneStepDef returns a 1-step definition whose Run records invocation via the
// supplied callback and returns ok.
func oneStepDef(defID idutil.SafeID, onRun func()) *ksaga.Definition {
	return &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{{
			Name: "step1",
			Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
				if onRun != nil {
					onRun()
				}
				return []byte(`{}`), nil
			},
		}},
	}
}

// newLeaderElectCoordinator builds a Coordinator wired with WithLeaderElect over
// the given locker, sharing the supplied journal/clock so multiple coordinators
// can contend.
func newLeaderElectCoordinator(
	t *testing.T, j journal.Journal, clk *clockmock.FakeClock, reg ksaga.Resolver, locker distlock.Locker,
) (*Coordinator, *recordingDispatcher) {
	t.Helper()
	disp := &recordingDispatcher{}
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(leaderElectCfg()), WithDispatcher(disp), WithLeaderElect(locker))
	if err != nil {
		t.Fatalf("NewCoordinator(WithLeaderElect): %v", err)
	}
	return c, disp
}

// claimedFixture returns a ClaimedInstance with deterministic IDs
// (def1/inst1 → lock key "saga:4:def1:inst1") for direct acquireLead unit tests.
func claimedFixture(now time.Time) journal.ClaimedInstance {
	return journal.ClaimedInstance{
		Instance: ksaga.NewInstance("inst1", "def1", now),
		LeaseID:  "lease-inst1",
	}
}

// ---------------------------------------------------------------------------
// nil locker fail-fast
// ---------------------------------------------------------------------------

// nilTestLocker is a concrete Locker used only to construct a typed-nil value
// ((*nilTestLocker)(nil)) for the typed-nil branch of the fail-fast test.
type nilTestLocker struct{}

func (*nilTestLocker) Acquire(context.Context, string, time.Duration) (*distlock.Lock, error) {
	panic("nilTestLocker.Acquire must not be called (typed-nil construction only)")
}
func (*nilTestLocker) Stats() distlock.Stats { return distlock.Stats{} }

// TestWithLeaderElect_NilLocker_FailFast asserts that WithLeaderElect rejects
// both a bare-nil and a typed-nil locker at NewCoordinator (strong-dependency
// wiring option: nil → leaderElectNil sentinel → fail-fast).
func TestWithLeaderElect_NilLocker_FailFast(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}
	reg, err := ksaga.NewInMemoryRegistry()
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}

	tests := []struct {
		name   string
		locker distlock.Locker
	}{
		{"bare_nil", nil},
		{"typed_nil", (*nilTestLocker)(nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
				WithLeaderElect(tc.locker))
			if err == nil {
				t.Fatalf("NewCoordinator(WithLeaderElect(%s)) = nil error, want fail-fast", tc.name)
			}
			if !strings.Contains(err.Error(), "locker") {
				t.Errorf("error = %q, want mention of locker", err.Error())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// acquireLead — single-process default
// ---------------------------------------------------------------------------

// TestAcquireLead_SingleProcess_AlwaysLeads asserts that a Coordinator without
// WithLeaderElect (locker == nil) always leads — acquireLead returns a non-nil
// no-op release and lead=true.
func TestAcquireLead_SingleProcess_AlwaysLeads(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry()
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(leaderElectCfg()))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if c.locker != nil {
		t.Fatal("expected nil locker for single-process coordinator")
	}
	release, orphan, lead := c.acquireLead(context.Background(), claimedFixture(clk.Now()))
	if !lead {
		t.Fatal("single-process acquireLead lead=false, want true")
	}
	if release == nil {
		t.Fatal("single-process acquireLead release=nil, want non-nil no-op")
	}
	if orphan == nil {
		t.Fatal("single-process acquireLead orphan=nil, want non-nil no-op")
	}
	release() // must not panic
	orphan()  // must not panic
}

// ---------------------------------------------------------------------------
// acquireLead — distlock mutual exclusion + key format
// ---------------------------------------------------------------------------

// TestAcquireLead_MutualExclusion asserts that two coordinators sharing one
// FakeDriver cannot both hold the same instance's lock: the second acquireLead
// is skipped (lead=false) until the first releases. Also asserts the lock key
// format saga:{definitionID}:{instanceID}.
func TestAcquireLead_MutualExclusion(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker1, err := distlock.New(fd, clk)
	if err != nil {
		t.Fatalf("distlock.New 1: %v", err)
	}
	locker2, err := distlock.New(fd, clk)
	if err != nil {
		t.Fatalf("distlock.New 2: %v", err)
	}
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry()
	c1, _ := newLeaderElectCoordinator(t, j, clk, reg, locker1)
	c2, _ := newLeaderElectCoordinator(t, j, clk, reg, locker2)

	ctx := context.Background()
	ci := claimedFixture(clk.Now())

	// c1 acquires.
	release1, _, lead1 := c1.acquireLead(ctx, ci)
	if !lead1 {
		t.Fatal("c1 acquireLead lead=false, want true")
	}

	// Key format assertion.
	wantKey := leaderElectLockKey(ci.Instance.DefinitionID, ci.Instance.ID)
	if _, ok := fd.Snapshot()[wantKey]; !ok {
		t.Errorf("FakeDriver key %q not held; snapshot=%v", wantKey, fd.Snapshot())
	}

	// c2 cannot acquire the same instance.
	release2, orphan2, lead2 := c2.acquireLead(ctx, ci)
	if lead2 {
		t.Fatal("c2 acquireLead lead=true while c1 holds the lock, want false (skip)")
	}
	if release2 != nil {
		t.Error("c2 acquireLead release must be nil when lead=false")
	}
	if orphan2 != nil {
		t.Error("c2 acquireLead orphan must be nil when lead=false")
	}

	// c1 releases; c2 can now acquire.
	release1()
	release3, _, lead3 := c2.acquireLead(ctx, ci)
	if !lead3 {
		t.Fatal("c2 acquireLead lead=false after c1 release, want true")
	}
	release3()
}

// errSetNXDriver is a distlock.Driver whose SetNX always returns an I/O error,
// used to exercise acquireLead's fail-closed path (skip on non-timeout error).
type errSetNXDriver struct{}

func (errSetNXDriver) SetNX(context.Context, string, string, time.Duration) (bool, error) {
	return false, errors.New("simulated SetNX I/O failure")
}

func (errSetNXDriver) Renew(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}
func (errSetNXDriver) Release(context.Context, string, string) error { return nil }

// TestAcquireLead_IOError_FailClosed asserts that when Acquire fails with a
// non-timeout (I/O) error, acquireLead returns lead=false (fail-closed: do not
// drive without confirmed leadership).
func TestAcquireLead_IOError_FailClosed(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	locker, err := distlock.New(errSetNXDriver{}, clk)
	if err != nil {
		t.Fatalf("distlock.New: %v", err)
	}
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry()
	c, _ := newLeaderElectCoordinator(t, j, clk, reg, locker)

	release, orphan, lead := c.acquireLead(context.Background(), claimedFixture(clk.Now()))
	if lead {
		t.Fatal("acquireLead lead=true on I/O error, want false (fail-closed)")
	}
	if release != nil {
		t.Error("acquireLead release must be nil when lead=false")
	}
	if orphan != nil {
		t.Error("acquireLead orphan must be nil when lead=false")
	}
}

// ---------------------------------------------------------------------------
// tickOnce — gate integration (skip vs drive)
// ---------------------------------------------------------------------------

// TestTickOnce_SkipsWhenLockHeld asserts that when another holder owns the
// instance's distlock, tickOnce claims the instance but skips driving it: the
// journal is not advanced and the dispatcher is not kicked.
func TestTickOnce_SkipsWhenLockHeld(t *testing.T) {
	t.Parallel()
	const defID idutil.SafeID = "skipdef"
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker, _ := distlock.New(fd, clk)
	blocker, _ := distlock.New(fd, clk)
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry(oneStepDef(defID, nil))
	c, disp := newLeaderElectCoordinator(t, j, clk, reg, locker)

	ctx := context.Background()
	inst := ksaga.NewInstance("skipinst", defID, clk.Now())
	if err := j.Enqueue(ctx, inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Another process holds the instance lock.
	key := leaderElectLockKey(inst.DefinitionID, inst.ID)
	held, err := blocker.Acquire(ctx, key, leaderElectCfg().LeaseDuration)
	if err != nil {
		t.Fatalf("blocker.Acquire: %v", err)
	}
	defer func() { _ = held.Release() }()

	if err := c.tickOnce(ctx); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}

	evs, err := j.Load(ctx, inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("journal advanced to %d events while lock held by another holder, want 0", len(evs))
	}
	if got := disp.KickCount(); got != 0 {
		t.Errorf("dispatcher KickCount = %d, want 0 (instance skipped)", got)
	}
}

// TestTickOnce_DrivesAndReleasesWhenLockFree asserts that when the lock is
// free, tickOnce acquires it, drives the instance to terminal, and releases the
// lock afterwards (FakeDriver Release called; key no longer held).
func TestTickOnce_DrivesAndReleasesWhenLockFree(t *testing.T) {
	t.Parallel()
	const defID idutil.SafeID = "drivedef"
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker, _ := distlock.New(fd, clk)
	j, _ := journal.NewMemJournal(clk)
	ran := false
	reg, _ := ksaga.NewInMemoryRegistry(oneStepDef(defID, func() { ran = true }))
	c, disp := newLeaderElectCoordinator(t, j, clk, reg, locker)

	ctx := context.Background()
	inst := ksaga.NewInstance("driveinst", defID, clk.Now())
	if err := j.Enqueue(ctx, inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := c.tickOnce(ctx); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}

	if !ran {
		t.Error("Step.Run was not executed despite a free lock")
	}
	evs, err := j.Load(ctx, inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) != 2 { // StepCompleted + SagaSucceeded
		t.Errorf("journal event count = %d, want 2", len(evs))
	}
	if got := disp.KickCount(); got != 1 {
		t.Errorf("dispatcher KickCount = %d, want 1", got)
	}
	// Lock released after the drive: key no longer held, Release was called.
	if len(fd.Snapshot()) != 0 {
		t.Errorf("lock still held after drive; snapshot=%v", fd.Snapshot())
	}
	if got := fd.Calls("Release"); got < 1 {
		t.Errorf("FakeDriver Release calls = %d, want >= 1", got)
	}
}

// ---------------------------------------------------------------------------
// Start warn label
// ---------------------------------------------------------------------------

// TestStart_LeaderElect_EmitsLeaderElectMode asserts that a leader-elect
// coordinator logs mode=leader_elect at Start (and NOT the single-process
// unsafe_no_leader warning).
func TestStart_LeaderElect_EmitsLeaderElectMode(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker, _ := distlock.New(fd, clk)
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry()

	// syncBuffer (not bare bytes.Buffer): the testwait closure below reads the
	// buffer from this goroutine while the Start() goroutine writes the start
	// log via slog — bytes.Buffer is not concurrency-safe (go test -race race).
	var buf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(leaderElectCfg()), WithLeaderElect(locker), WithLogger(logger))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	testwait.Deterministic(t, c.Ready(), "coordinator-ready")
	// Wait for the start log line to be flushed.
	testwait.External(t, "start-log-flushed",
		func() bool { return strings.Contains(buf.String(), LeaderElectModeLabel) },
		testtime.D2s, testtime.D2ms)

	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer stopCancel()
	if err := c.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Stop: %v", err)
	}
	_ = testwait.Deterministic(t, done, "start-goroutine-exit")

	logs := buf.String()
	if !strings.Contains(logs, LeaderElectModeLabel) {
		t.Errorf("start logs missing mode=%s; logs=%s", LeaderElectModeLabel, logs)
	}
	if strings.Contains(logs, UnsafeModeLabel) {
		t.Errorf("start logs contain %s for a leader-elect coordinator; logs=%s", UnsafeModeLabel, logs)
	}
}

// ---------------------------------------------------------------------------
// log-level discipline
// ---------------------------------------------------------------------------

// captureLeaderCoord builds a leader-elect coordinator whose logger writes JSON
// to buf at Debug level, for log-level assertions. Not started — acquireLead /
// release run without the tick loop.
func captureLeaderCoord(t *testing.T, clk *clockmock.FakeClock, locker distlock.Locker, buf *bytes.Buffer) *Coordinator {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry()
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(leaderElectCfg()), WithLeaderElect(locker), WithLogger(logger))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

// makeContentionLocker builds a Locker whose next SetNX returns false (simulates
// contention / ErrLockTimeout).
func makeContentionLocker(t *testing.T, clk *clockmock.FakeClock) distlock.Locker {
	t.Helper()
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	fd.SetNextSetNX(false)
	l, err := distlock.New(fd, clk)
	if err != nil {
		t.Fatalf("distlock.New: %v", err)
	}
	return l
}

// makeNormalLocker builds a Locker backed by a default FakeDriver.
func makeNormalLocker(t *testing.T, clk *clockmock.FakeClock) distlock.Locker {
	t.Helper()
	l, err := distlock.New(locktest.NewFakeDriverWithClock(clk.Now), clk)
	if err != nil {
		t.Fatalf("distlock.New: %v", err)
	}
	return l
}

// makeErrLocker builds a Locker whose SetNX always returns an I/O error.
func makeErrLocker(t *testing.T, clk *clockmock.FakeClock) distlock.Locker {
	t.Helper()
	l, err := distlock.New(errSetNXDriver{}, clk)
	if err != nil {
		t.Fatalf("distlock.New: %v", err)
	}
	return l
}

// assertLeaderSkipLog checks that the captured log contains the expected level
// and the skip message.
func assertLeaderSkipLog(t *testing.T, logs, wantLevel string) {
	t.Helper()
	if !strings.Contains(logs, `"level":"`+wantLevel+`"`) {
		t.Errorf("want level %s; logs=%s", wantLevel, logs)
	}
	if !strings.Contains(logs, "leader-elect skip") {
		t.Errorf("missing skip message; logs=%s", logs)
	}
}

// TestLogLeaderSkip_Levels asserts acquireLead logs the skip at the level
// matching its cause: contention (ErrLockTimeout) and ctx cancellation → Debug
// (expected operational signals); backend I/O error → Warn (fault).
func TestLogLeaderSkip_Levels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		locker    func(t *testing.T, clk *clockmock.FakeClock) distlock.Locker
		ctx       func() context.Context
		wantLevel string
	}{
		{
			name:      "contention_debug",
			locker:    makeContentionLocker,
			ctx:       context.Background,
			wantLevel: "DEBUG",
		},
		{
			name:   "ctx_canceled_debug",
			locker: makeNormalLocker,
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantLevel: "DEBUG",
		},
		{
			name:      "io_error_warn",
			locker:    makeErrLocker,
			ctx:       context.Background,
			wantLevel: "WARN",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
			var buf bytes.Buffer
			c := captureLeaderCoord(t, clk, tc.locker(t, clk), &buf)
			_, _, lead := c.acquireLead(tc.ctx(), claimedFixture(clk.Now()))
			if lead {
				t.Fatal("expected lead=false (acquire should fail)")
			}
			assertLeaderSkipLog(t, buf.String(), tc.wantLevel)
		})
	}
}

// TestAcquireLead_ReleaseFail_LogsWarn asserts the release closure logs a Warn
// (with definition_id) when lock.Release() fails (FakeDriver-injected error).
func TestAcquireLead_ReleaseFail_LogsWarn(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker, err := distlock.New(fd, clk)
	if err != nil {
		t.Fatalf("distlock.New: %v", err)
	}
	var buf bytes.Buffer
	c := captureLeaderCoord(t, clk, locker, &buf)

	release, _, lead := c.acquireLead(context.Background(), claimedFixture(clk.Now()))
	if !lead {
		t.Fatal("expected lead=true")
	}
	fd.SetNextReleaseError(errors.New("simulated release I/O failure"))
	release()

	logs := buf.String()
	if !strings.Contains(logs, "distlock release failed") {
		t.Errorf("missing release-failed log; logs=%s", logs)
	}
	if !strings.Contains(logs, `"level":"WARN"`) {
		t.Errorf("release failure should log WARN; logs=%s", logs)
	}
	if !strings.Contains(logs, `"definition_id":"def1"`) {
		t.Errorf("release-failed log missing definition_id; logs=%s", logs)
	}
}

// ---------------------------------------------------------------------------
// test infra (PR #1108 review fixes)
// ---------------------------------------------------------------------------

// syncBuffer is a mutex-guarded io.Writer + String() for tests that read the
// log buffer from one goroutine while a Coordinator goroutine writes slog
// records to it. bytes.Buffer is NOT safe for concurrent Write/String
// (go test -race data race) — see TestStart_LeaderElect_EmitsLeaderElectMode.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------------------
// F5 — leader-elect rejects sub-millisecond LeaseDuration (distlock TTL floor)
// ---------------------------------------------------------------------------

// TestNewCoordinator_LeaderElect_RejectsSubMillisLease asserts that enabling
// leader election with a sub-millisecond LeaseDuration fails fast at
// construction (distlock.Acquire requires TTL >= distlock.MinTTL; without the
// guard the coordinator would construct fine but every acquireLead would fail
// at runtime → silently never drive). Single-process mode (no locker) must
// still accept a sub-ms lease since distlock is not involved.
// Sub-ms durations for the leader-elect fail-fast test, below distlock.MinTTL
// (1ms). Site-specific (no cross-cutting testtime const is sub-ms), declared as
// package-level consts per TEST-TIME-LITERAL-01. They satisfy Config.Validate
// (all > 0, PollInterval > 0, LeaseDuration > 0).
const (
	subMsLease     = 500 * time.Microsecond
	subMsPoll      = 200 * time.Microsecond
	subMsHeartbeat = 100 * time.Microsecond // satisfies HeartbeatInterval*HeartbeatLeaseSafetyFactor < LeaseDuration
)

func TestNewCoordinator_LeaderElect_RejectsSubMillisLease(t *testing.T) {
	t.Parallel()
	subMsCfg := Config{
		PollInterval:      subMsPoll,
		ClaimBatchSize:    16,
		LeaseDuration:     subMsLease,
		HeartbeatInterval: subMsHeartbeat,
	}
	if err := subMsCfg.Validate(); err != nil {
		t.Fatalf("precondition: sub-ms cfg must pass Config.Validate, got %v", err)
	}

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker, _ := distlock.New(fd, clk)
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry()

	// Leader-elect mode → must fail fast (distlock MinTTL floor). NewCoordinator
	// constructs its internal Executor from the same journal + subMsCfg so the
	// sub-ms HeartbeatInterval / LeaseDuration are carried into the Executor
	// automatically (#1181 F5).
	if _, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(subMsCfg), WithLeaderElect(locker)); err == nil {
		t.Error("NewCoordinator(leader-elect, sub-ms lease) = nil error, want fail-fast")
	} else if !strings.Contains(err.Error(), "LeaseDuration") {
		t.Errorf("error = %q, want mention of LeaseDuration", err.Error())
	}

	// Single-process mode → sub-ms lease is fine (distlock not used).
	if _, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithConfig(subMsCfg)); err != nil {
		t.Errorf("NewCoordinator(single-process, sub-ms lease) = %v, want nil (distlock floor must not apply)", err)
	}
}

// ---------------------------------------------------------------------------
// F2 — lock key must be injective over SafeID's full charset
// ---------------------------------------------------------------------------

// TestLeaderElectLockKey_Injective asserts the lock-key builder is injective:
// distinct (definitionID, instanceID) pairs must never collide. idutil.SafeID
// permits ':' and '/' (id.go IsSafeID), so a naive "saga:{def}:{inst}" join is
// ambiguous — (def="a:b", inst="c") and (def="a", inst="b:c") both yield the
// same string. instance IDs are caller-supplied SafeIDs (NewInstance), so the
// key must be injective over the full charset, not the current generator output.
func TestLeaderElectLockKey_Injective(t *testing.T) {
	t.Parallel()
	collisionPairs := []struct {
		defA, instA, defB, instB idutil.SafeID
	}{
		{"a:b", "c", "a", "b:c"},
		{"x:y:z", "w", "x", "y:z:w"},
		{"def:1", "inst", "def", "1:inst"},
	}
	for _, p := range collisionPairs {
		ka := leaderElectLockKey(p.defA, p.instA)
		kb := leaderElectLockKey(p.defB, p.instB)
		if ka == kb {
			t.Errorf("lock key collision: (%q,%q) and (%q,%q) both → %q; "+
				"key builder must be injective over SafeID's full charset",
				p.defA, p.instA, p.defB, p.instB, ka)
		}
	}
	// Distinct instances of the same definition still differ.
	if leaderElectLockKey("d", "i1") == leaderElectLockKey("d", "i2") {
		t.Error("distinct instance IDs produced the same lock key")
	}
}

// ---------------------------------------------------------------------------
// F1 — Stop orphans in-flight distlocks (bounded-TTL handoff, no shutdown I/O)
// ---------------------------------------------------------------------------

// TestStop_OrphansInflightLockOnShutdown asserts that when Stop's drain budget
// is exhausted by a non-cooperative step (one that ignores ctx.Done()), the
// in-flight distlock is orphaned — renewal is stopped without performing a
// Driver.Release RPC — so another coordinator can take over once the lease
// lapses (~1×TTL from the last successful renewal, best-effort).
//
// Key assertions:
//   - fd.Calls("Release") == 0 after Stop: Orphan must NOT have performed a
//     Driver.Release I/O for the in-flight lock (the whole point of the
//     bounded-TTL no-I/O handoff). The FakeDriver records Release calls; if
//     Orphan triggered a Release, the count would be > 0.
//   - fd.Snapshot() still has the key after Stop: Orphan intentionally does NOT
//     delete the backend key (the key expires on its lease TTL — ~1×TTL from the
//     last successful renewal). A competitor coordinator can acquire it once the
//     lease lapses. This is the
//     bounded-TTL handoff guarantee — no Release I/O means no blocking on an
//     unreachable backend during shutdown.
func TestStop_OrphansInflightLockOnShutdown(t *testing.T) {
	t.Parallel()
	const defID idutil.SafeID = "stopdef"

	stepEntered := make(chan struct{})
	stepBlockCh := make(chan struct{})
	var enterOnce sync.Once
	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{{
			Name: "wedgedstep",
			// Non-cooperative: blocks ONLY on stepBlockCh, never selects on
			// ctx.Done(). A cooperative step would return on cancel and release
			// the lock the normal way — not exercising the F1 fix.
			Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
				enterOnce.Do(func() { close(stepEntered) })
				<-stepBlockCh
				return []byte(`{}`), nil
			},
		}},
	}

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker, _ := distlock.New(fd, clk)
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry(def)
	c, _ := newLeaderElectCoordinator(t, j, clk, reg, locker)

	inst := ksaga.NewInstance("stopinst", defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(ctx) }()
	testwait.Deterministic(t, c.Ready(), "coordinator-ready")

	testwait.External(t, "tickers-registered",
		func() bool { return clk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)
	clk.Advance(leaderElectCfg().PollInterval) // fire a tick → claim + wedge

	testwait.Deterministic(t, stepEntered, "step-entered")
	if len(fd.Snapshot()) != 1 {
		t.Fatalf("distlock not held while drive in-flight; snapshot=%v", fd.Snapshot())
	}
	// Reset Release counter so only Stop-path calls are counted.
	fd.ResetCalls()

	// Short Stop budget: the wedged step outlives drain, forcing the explicit
	// orphan path. Stop returns a drain-timeout error; the lock orphan is
	// what we assert.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D200ms)
	defer stopCancel()
	_ = c.Stop(stopCtx)

	// Orphan must NOT have called Driver.Release (bounded-TTL handoff, I/O-free).
	if got := fd.Calls("Release"); got != 0 {
		t.Errorf("Stop called Driver.Release %d time(s); want 0 — Stop must orphan (no I/O), not release", got)
	}
	// The backend key is intentionally left in the FakeDriver after Orphan:
	// renewal was stopped but the key expires via TTL. A competitor coordinator
	// can acquire it once the lease lapses.
	snap := fd.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected FakeDriver to still hold the key after Orphan (backend key left for TTL expiry); snapshot=%v", snap)
	}
	var orphanedKey string
	for k := range snap {
		orphanedKey = k
	}

	// F5: prove the orphan actually STOPPED renewal — advancing the clock past a
	// full lease window must trigger no further Driver.Renew. A live renewal loop
	// would have renewed at renewFraction×TTL (< TTL) within this window.
	renewAfterStop := fd.Calls("Renew")
	clk.Advance(leaderElectCfg().LeaseDuration)
	if got := fd.Calls("Renew"); got != renewAfterStop {
		t.Errorf("Driver.Renew called %d time(s) after Stop orphan; want %d — renewal must stop on orphan", got, renewAfterStop)
	}

	// F5: prove TTL-expiry takeover — once the orphaned lease lapses (clock now
	// past expiresAt), a competitor wins SetNX on the same key, with no Release
	// ever having occurred (asserted above). This is the bounded-TTL handoff.
	acquired, err := fd.SetNX(context.Background(), orphanedKey, "competitor-token", leaderElectCfg().LeaseDuration)
	if err != nil {
		t.Fatalf("competitor SetNX after TTL expiry: %v", err)
	}
	if !acquired {
		t.Errorf("competitor could not acquire orphaned key %q after TTL expiry; orphan handoff broken", orphanedKey)
	}

	// Cleanup: unblock the step + cancel so goroutines drain (goleak TestMain).
	close(stepBlockCh)
	cancel()
	_ = testwait.Deterministic(t, startDone, "start-goroutine-exit")
}

// TestStop_OrphansBeforeCancel_CooperativeStepNoRelease asserts the F2 ordering
// guarantee: Stop orphans in-flight distlocks BEFORE canceling the drive ctx, so
// a cooperative step woken by cancel cannot race a release() (Driver.Release RPC)
// ahead of orphan(). The step here selects on ctx.Done() and would normally
// release on cancel; because orphan() consumes the lock's shared sync.Once first,
// that later release() is a no-op and Driver.Release is never called.
//
// Without the orphan-before-cancel ordering this assertion is racy: cancel()
// would wake the step, whose tickOnce could win the sync.Once with release() and
// drive a Driver.Release RPC — defeating the I/O-free-shutdown guarantee. With
// the ordering, orphan() completes synchronously before cancel(), so Release==0
// is deterministic.
func TestStop_OrphansBeforeCancel_CooperativeStepNoRelease(t *testing.T) {
	t.Parallel()
	const defID idutil.SafeID = "coopstopdef"

	stepEntered := make(chan struct{})
	stepBlockCh := make(chan struct{})
	var enterOnce sync.Once
	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{{
			Name: "coopstep",
			// Cooperative: returns on ctx.Done(). It does NOT finish during the
			// drain window (ctx not yet canceled, stepBlockCh not closed), so it is
			// still in-flight when Stop reaches the orphan/cancel point — then
			// cancel() wakes it and it would call release() the normal way.
			Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
				enterOnce.Do(func() { close(stepEntered) })
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-stepBlockCh:
					return []byte(`{}`), nil
				}
			},
		}},
	}

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker, _ := distlock.New(fd, clk)
	j, _ := journal.NewMemJournal(clk)
	reg, _ := ksaga.NewInMemoryRegistry(def)
	c, _ := newLeaderElectCoordinator(t, j, clk, reg, locker)

	inst := ksaga.NewInstance("coopstopinst", defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(ctx) }()
	testwait.Deterministic(t, c.Ready(), "coordinator-ready")

	testwait.External(t, "tickers-registered",
		func() bool { return clk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)
	clk.Advance(leaderElectCfg().PollInterval) // fire a tick → claim + drive

	testwait.Deterministic(t, stepEntered, "step-entered")
	if len(fd.Snapshot()) != 1 {
		t.Fatalf("distlock not held while drive in-flight; snapshot=%v", fd.Snapshot())
	}
	// Reset Release counter so only Stop-path calls are counted.
	fd.ResetCalls()

	// Short Stop budget: the cooperative step outlives drain (it only returns on
	// cancel, which Stop issues AFTER orphan). Stop returns a drain-timeout error.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D200ms)
	defer stopCancel()
	_ = c.Stop(stopCtx)

	// Orphan ran before cancel woke the step → its release() is a no-op → no I/O.
	if got := fd.Calls("Release"); got != 0 {
		t.Errorf("Stop called Driver.Release %d time(s) for a cooperative step; want 0 — "+
			"orphan must run before cancel so release() races are no-ops", got)
	}

	// Cleanup: cancel so the (already-canceled) coordinator goroutines drain.
	cancel()
	_ = testwait.Deterministic(t, startDone, "start-goroutine-exit")
}

// TestTickOnce_NormalCompletion_ReleasesNotOrphans asserts that when a step
// completes normally (cooperative, not drain-budget-exhausted), tickOnce calls
// release (Driver.Release I/O) and NOT orphan. This is the "work done →
// immediate release" fast-path.
func TestTickOnce_NormalCompletion_ReleasesNotOrphans(t *testing.T) {
	t.Parallel()
	const defID idutil.SafeID = "normaldef"
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	fd := locktest.NewFakeDriverWithClock(clk.Now)
	locker, _ := distlock.New(fd, clk)
	j, _ := journal.NewMemJournal(clk)
	ran := false
	reg, _ := ksaga.NewInMemoryRegistry(oneStepDef(defID, func() { ran = true }))
	c, _ := newLeaderElectCoordinator(t, j, clk, reg, locker)

	ctx := context.Background()
	inst := ksaga.NewInstance("normalinst", defID, clk.Now())
	if err := j.Enqueue(ctx, inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := c.tickOnce(ctx); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}

	if !ran {
		t.Error("Step.Run was not executed")
	}
	// Normal completion: Release must have been called (immediate handoff).
	if got := fd.Calls("Release"); got < 1 {
		t.Errorf("normal completion: Driver.Release calls = %d, want >= 1", got)
	}
	// Lock must be free after the drive.
	if len(fd.Snapshot()) != 0 {
		t.Errorf("lock still held after normal completion; snapshot=%v", fd.Snapshot())
	}
}
