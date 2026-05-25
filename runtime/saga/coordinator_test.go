package saga

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// File-local duration consts for values not present in testtime.
const (
	// testHeartbeatValid is a heartbeat interval that satisfies
	// HeartbeatInterval*2 < LeaseDuration (9*2=18 < 30).
	testHeartbeatValid = 9 * time.Second
	// testNegativeDuration is used to test that negative PollInterval is rejected.
	testNegativeDuration = -testtime.D1ms
)

// ---------------------------------------------------------------------------
// Minimal in-test fakes (will be reused/shared with integration tests in
// Batch 3 — declared in coordinator_test.go for white-box access to package saga)
// ---------------------------------------------------------------------------

// fakeTxRunner is a minimal TxRunner that runs fn inline with an
// after-commit registry installed. Required for driveOne tests.
type fakeTxRunner struct {
	err error // if non-nil, RunInTx returns this error (without calling fn)
}

func (f *fakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	if f.err != nil {
		return f.err
	}
	ctx, installed := persistence.WithAfterCommitRegistry(ctx)
	err := fn(ctx)
	if err != nil {
		persistence.TruncateAfterCommitTo(ctx, 0)
		return err
	}
	if installed {
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

// fakeEmitter records Emit calls; it satisfies koutbox.Emitter.
type fakeEmitter struct {
	entries []koutbox.Entry
	err     error
}

func (f *fakeEmitter) Emit(_ context.Context, e koutbox.Entry) error {
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, e)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newFakeClock returns a deterministic FakeClock anchored at a fixed epoch.
func newFakeClock() *clockmock.FakeClock {
	return clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
}

// newMemJournal creates a MemJournal backed by clk (panics on error —
// test helper only).
func newMemJournal(clk clock.Clock) *journal.MemJournal {
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		panic(err)
	}
	return j
}

// newRegistry creates an empty ksaga.InMemoryRegistry (panics on error —
// test helper only).
func newRegistry() *ksaga.InMemoryRegistry {
	r, err := ksaga.NewInMemoryRegistry()
	if err != nil {
		panic(err)
	}
	return r
}

// noopStep is a StepFunc that succeeds immediately and returns nil state.
func noopStep(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// TestDefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.PollInterval != testtime.D200ms {
		t.Errorf("PollInterval = %v, want 200ms", cfg.PollInterval)
	}
	if cfg.ClaimBatchSize != 16 {
		t.Errorf("ClaimBatchSize = %d, want 16", cfg.ClaimBatchSize)
	}
	if cfg.LeaseDuration != testtime.D30s {
		t.Errorf("LeaseDuration = %v, want 30s", cfg.LeaseDuration)
	}
	if cfg.HeartbeatInterval != testtime.D10s {
		t.Errorf("HeartbeatInterval = %v, want 10s", cfg.HeartbeatInterval)
	}
}

// ---------------------------------------------------------------------------
// TestConfig_Validate
// ---------------------------------------------------------------------------

func TestConfig_Validate(t *testing.T) {
	valid := DefaultConfig()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "all defaults valid",
			cfg:     valid,
			wantErr: false,
		},
		{
			name: "zero PollInterval",
			cfg: Config{
				PollInterval:      0,
				ClaimBatchSize:    16,
				LeaseDuration:     testtime.D30s,
				HeartbeatInterval: testtime.D10s,
			},
			wantErr: true,
		},
		{
			name: "negative PollInterval",
			cfg: Config{
				PollInterval:      testNegativeDuration,
				ClaimBatchSize:    16,
				LeaseDuration:     testtime.D30s,
				HeartbeatInterval: testtime.D10s,
			},
			wantErr: true,
		},
		{
			name: "zero ClaimBatchSize",
			cfg: Config{
				PollInterval:      testtime.D200ms,
				ClaimBatchSize:    0,
				LeaseDuration:     testtime.D30s,
				HeartbeatInterval: testtime.D10s,
			},
			wantErr: true,
		},
		{
			name: "negative ClaimBatchSize",
			cfg: Config{
				PollInterval:      testtime.D200ms,
				ClaimBatchSize:    -1,
				LeaseDuration:     testtime.D30s,
				HeartbeatInterval: testtime.D10s,
			},
			wantErr: true,
		},
		{
			name: "zero LeaseDuration",
			cfg: Config{
				PollInterval:      testtime.D200ms,
				ClaimBatchSize:    16,
				LeaseDuration:     0,
				HeartbeatInterval: testtime.D10s,
			},
			wantErr: true,
		},
		{
			name: "zero HeartbeatInterval",
			cfg: Config{
				PollInterval:      testtime.D200ms,
				ClaimBatchSize:    16,
				LeaseDuration:     testtime.D30s,
				HeartbeatInterval: 0,
			},
			wantErr: true,
		},
		{
			name: "HeartbeatInterval*2 == LeaseDuration",
			cfg: Config{
				PollInterval:      testtime.D200ms,
				ClaimBatchSize:    16,
				LeaseDuration:     testtime.D20s,
				HeartbeatInterval: testtime.D10s, // 10*2 == 20 → invalid
			},
			wantErr: true,
		},
		{
			name: "HeartbeatInterval*2 > LeaseDuration",
			cfg: Config{
				PollInterval:      testtime.D200ms,
				ClaimBatchSize:    16,
				LeaseDuration:     testtime.D15s,
				HeartbeatInterval: testtime.D10s, // 10*2 > 15 → invalid
			},
			wantErr: true,
		},
		{
			name: "HeartbeatInterval*2 < LeaseDuration OK",
			cfg: Config{
				PollInterval:      testtime.D200ms,
				ClaimBatchSize:    16,
				LeaseDuration:     testtime.D30s,
				HeartbeatInterval: testHeartbeatValid, // 9*2 < 30 → valid
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestNewCoordinator_NilDeps
// ---------------------------------------------------------------------------

func TestNewCoordinator_NilDeps(t *testing.T) {
	clk := newFakeClock()
	j := newMemJournal(clk)
	tx := &fakeTxRunner{}
	em := &fakeEmitter{}
	reg := newRegistry()

	tests := []struct {
		name       string
		j          journal.Journal
		tx         persistence.TxRunner
		em         koutbox.Emitter
		reg        ksaga.Registry
		wantErrStr string
	}{
		{
			name:       "nil journal",
			j:          nil,
			tx:         tx,
			em:         em,
			reg:        reg,
			wantErrStr: "runtime/saga: journal required",
		},
		{
			name:       "nil txRunner",
			j:          j,
			tx:         nil,
			em:         em,
			reg:        reg,
			wantErrStr: "runtime/saga: txRunner required",
		},
		{
			name:       "nil outboxEmit",
			j:          j,
			tx:         tx,
			em:         nil,
			reg:        reg,
			wantErrStr: "runtime/saga: outboxEmit required",
		},
		{
			name:       "nil registry",
			j:          j,
			tx:         tx,
			em:         em,
			reg:        nil,
			wantErrStr: "runtime/saga: registry required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewCoordinator(tt.j, tt.tx, tt.em, tt.reg, clk)
			if c != nil {
				t.Error("expected nil Coordinator on error")
			}
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			var ecErr *errcode.Error
			if !errors.As(err, &ecErr) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ecErr.Kind != errcode.KindInvalid {
				t.Errorf("kind = %v, want KindInvalid", ecErr.Kind)
			}
			if ecErr.Message != tt.wantErrStr {
				t.Errorf("message = %q, want %q", ecErr.Message, tt.wantErrStr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestNewCoordinator_NilClock
// ---------------------------------------------------------------------------

func TestNewCoordinator_NilClock(t *testing.T) {
	clk := newFakeClock()
	j := newMemJournal(clk)
	tx := &fakeTxRunner{}
	em := &fakeEmitter{}
	reg := newRegistry()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from clock.MustHaveClock on nil clock")
		}
	}()
	_, _ = NewCoordinator(j, tx, em, reg, nil)
}

// ---------------------------------------------------------------------------
// TestNewCoordinator_HappyPath
// ---------------------------------------------------------------------------

func TestNewCoordinator_HappyPath(t *testing.T) {
	clk := newFakeClock()
	j := newMemJournal(clk)
	tx := &fakeTxRunner{}
	em := &fakeEmitter{}
	reg := newRegistry()

	c, err := NewCoordinator(j, tx, em, reg, clk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil Coordinator")
	}
}

// ---------------------------------------------------------------------------
// TestFoldEvents
// ---------------------------------------------------------------------------

func TestFoldEvents(t *testing.T) {
	// Build a 3-step definition for tests.
	def := &ksaga.Definition{
		ID: "test-def",
		Steps: []ksaga.Step{
			{Name: "step1", Run: noopStep},
			{Name: "step2", Run: noopStep},
			{Name: "step3", Run: noopStep},
		},
	}

	tests := []struct {
		name          string
		events        []journal.Event
		wantCursor    int
		wantPrevState []byte
		wantErr       bool
	}{
		{
			name:          "empty events → cursor 0",
			events:        []journal.Event{},
			wantCursor:    0,
			wantPrevState: nil,
			wantErr:       false,
		},
		{
			name: "1 StepCompleted → cursor 1, prevState from payload",
			events: []journal.Event{
				{Kind: journal.KindStepCompleted, StepName: "step1", Payload: []byte(`{"a":1}`)},
			},
			wantCursor:    1,
			wantPrevState: []byte(`{"a":1}`),
			wantErr:       false,
		},
		{
			name: "2 StepCompleted → cursor 2",
			events: []journal.Event{
				{Kind: journal.KindStepCompleted, StepName: "step1", Payload: []byte(`{"a":1}`)},
				{Kind: journal.KindStepCompleted, StepName: "step2", Payload: []byte(`{"b":2}`)},
			},
			wantCursor:    2,
			wantPrevState: []byte(`{"b":2}`),
			wantErr:       false,
		},
		{
			name: "3 StepCompleted (all steps done) → cursor 3",
			events: []journal.Event{
				{Kind: journal.KindStepCompleted, StepName: "step1", Payload: []byte(`{"a":1}`)},
				{Kind: journal.KindStepCompleted, StepName: "step2", Payload: []byte(`{"b":2}`)},
				{Kind: journal.KindStepCompleted, StepName: "step3", Payload: []byte(`{"c":3}`)},
			},
			wantCursor:    3,
			wantPrevState: []byte(`{"c":3}`),
			wantErr:       false,
		},
		{
			name: "KindStepFailed in history → errFoldEventMismatch",
			events: []journal.Event{
				{Kind: journal.KindStepCompleted, StepName: "step1", Payload: []byte(`{"a":1}`)},
				{Kind: journal.KindStepFailed, StepName: "step2"},
			},
			wantErr: true,
		},
		{
			name: "only KindStepFailed → errFoldEventMismatch",
			events: []journal.Event{
				{Kind: journal.KindStepFailed, StepName: "step1"},
			},
			wantErr: true,
		},
		// Terminal/compensation events in history are defensively ignored by foldEvents
		// (the Journal should have MarkTerminal'd so coordinator never re-claims such
		// instances in practice). The cursor stays at the last KindStepCompleted seen.
		{
			name: "KindCompensationStarted in history → skipped, cursor stays at prior StepCompleted",
			events: []journal.Event{
				{Kind: journal.KindStepCompleted, StepName: "step1", Payload: []byte(`{"a":1}`)},
				{Kind: journal.KindCompensationStarted},
			},
			wantCursor:    1,
			wantPrevState: []byte(`{"a":1}`),
			wantErr:       false,
		},
		{
			name: "KindSagaSucceeded in history → skipped, cursor from prior StepCompleted",
			events: []journal.Event{
				{Kind: journal.KindStepCompleted, StepName: "step1", Payload: []byte(`{"a":1}`)},
				{Kind: journal.KindSagaSucceeded},
			},
			wantCursor:    1,
			wantPrevState: []byte(`{"a":1}`),
			wantErr:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor, prevState, err := foldEvents(tt.events, def)
			if (err != nil) != tt.wantErr {
				t.Errorf("foldEvents() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if cursor != tt.wantCursor {
				t.Errorf("cursor = %d, want %d", cursor, tt.wantCursor)
			}
			if !bytes.Equal(prevState, tt.wantPrevState) {
				t.Errorf("prevState = %q, want %q", prevState, tt.wantPrevState)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestStartStop_Idempotency
// ---------------------------------------------------------------------------

func TestStartStop_Idempotency(t *testing.T) {
	clk := newFakeClock()
	c := mustCoordinator(t, clk)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D200ms)
	defer cancel()

	// Start in background.
	startErr := make(chan error, 1)
	go func() { startErr <- c.Start(ctx) }()

	// Wait for ready.
	select {
	case <-c.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for Ready()")
	}

	// Second Start should return KindConflict immediately.
	err := c.Start(ctx)
	if err == nil {
		t.Fatal("second Start should return error")
	}
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) || ecErr.Kind != errcode.KindConflict {
		t.Errorf("second Start error kind = %v, want KindConflict", err)
	}

	// Stop.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D500ms)
	defer stopCancel()
	if err := c.Stop(stopCtx); err != nil {
		t.Errorf("first Stop() error: %v", err)
	}

	// Second Stop is idempotent → nil.
	if err := c.Stop(stopCtx); err != nil {
		t.Errorf("second Stop() error: %v", err)
	}

	// Wait for Start to return (ctx expired or we stopped).
	select {
	case <-startErr:
	case <-time.After(testtime.D500ms):
		t.Fatal("Start goroutine did not return")
	}
}

// ---------------------------------------------------------------------------
// TestStartStop_LifecycleCAS
// ---------------------------------------------------------------------------

func TestStartStop_LifecycleCAS(t *testing.T) {
	clk := newFakeClock()
	c := mustCoordinator(t, clk)

	// First run: Start → running → Stop → stopped.
	run1Ctx, run1Cancel := context.WithTimeout(context.Background(), testtime.D200ms)
	defer run1Cancel()

	startErr := make(chan error, 1)
	go func() { startErr <- c.Start(run1Ctx) }()

	select {
	case <-c.Ready():
	case <-run1Ctx.Done():
		t.Fatal("timed out waiting for Ready() on first run")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D500ms)
	defer stopCancel()
	if err := c.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() run1: %v", err)
	}
	select {
	case <-startErr:
	case <-time.After(testtime.D500ms):
		t.Fatal("Start goroutine did not return after Stop")
	}

	// Second run: Start again → should succeed.
	run2Ctx, run2Cancel := context.WithTimeout(context.Background(), testtime.D200ms)
	defer run2Cancel()

	startErr2 := make(chan error, 1)
	go func() { startErr2 <- c.Start(run2Ctx) }()

	select {
	case <-c.Ready():
	case <-run2Ctx.Done():
		t.Fatal("timed out waiting for Ready() on second run")
	}

	stopCtx2, stopCancel2 := context.WithTimeout(context.Background(), testtime.D500ms)
	defer stopCancel2()
	if err := c.Stop(stopCtx2); err != nil {
		t.Fatalf("Stop() run2: %v", err)
	}
	select {
	case <-startErr2:
	case <-time.After(testtime.D500ms):
		t.Fatal("Start goroutine 2 did not return after Stop")
	}
}

// ---------------------------------------------------------------------------
// TestStart_Ready_Channel_Closes_After_State_Running
// ---------------------------------------------------------------------------

func TestStart_Ready_Channel_Closes_After_State_Running(t *testing.T) {
	clk := newFakeClock()
	c := mustCoordinator(t, clk)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D300ms)
	defer cancel()

	go func() { _ = c.Start(ctx) }()

	select {
	case <-c.Ready():
		// Good — Ready channel closed once coordinator is running.
		if coordState(c.state.Load()) != coordRunning {
			t.Errorf("state after Ready() = %v, want coordRunning", c.state.Load())
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Ready()")
	}
}

// ---------------------------------------------------------------------------
// TestRepoReady_Delegates_To_Journal
// ---------------------------------------------------------------------------

func TestRepoReady_Delegates_To_Journal(t *testing.T) {
	clk := newFakeClock()
	j := &stubRepoProberJournal{
		MemJournal: newMemJournal(clk),
		repoErr:    errors.New("repo down"),
	}
	tx := &fakeTxRunner{}
	em := &fakeEmitter{}
	reg := newRegistry()

	c, err := NewCoordinator(j, tx, em, reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	got := c.RepoReady(context.Background())
	if got == nil || got.Error() != "repo down" {
		t.Errorf("RepoReady() = %v, want error 'repo down'", got)
	}
}

// stubRepoProberJournal wraps MemJournal and overrides RepoReady.
type stubRepoProberJournal struct {
	*journal.MemJournal
	repoErr error
}

func (s *stubRepoProberJournal) RepoReady(_ context.Context) error {
	return s.repoErr
}

// ---------------------------------------------------------------------------
// helper: mustCoordinator
// ---------------------------------------------------------------------------

func mustCoordinator(t *testing.T, clk clock.Clock) *Coordinator {
	t.Helper()
	j := newMemJournal(clk)
	tx := &fakeTxRunner{}
	em := &fakeEmitter{}
	reg := newRegistry()
	c, err := NewCoordinator(j, tx, em, reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// F3 — TestStop_DrainsInflight / TestStop_DrainTimeout
// ---------------------------------------------------------------------------

// TestStop_DrainsInflight verifies that Stop() waits for an in-flight step to
// complete before returning, rather than immediately canceling goroutines.
// Strategy: enqueue an instance with a step that blocks on a channel, call Stop
// with a generous stopCtx, then unblock the channel. Stop should return only
// after the step completes.
func TestStop_DrainsInflight(t *testing.T) {
	const defID idutil.SafeID = "stopdrainsinflight"

	// stepBlockCh gates the step; close it to let the step finish.
	stepBlockCh := make(chan struct{})
	stepDone := make(chan struct{})

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "blockingstep",
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					defer close(stepDone)
					select {
					case <-stepBlockCh:
						return []byte(`{}`), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
			},
		},
	}

	clk := newFakeClock()
	j := newMemJournal(clk)
	reg, regErr := ksaga.NewInMemoryRegistry(def)
	if regErr != nil {
		t.Fatalf("NewInMemoryRegistry: %v", regErr)
	}
	em := newSafeFakeEmitter()
	tx := newSafeFakeTxRunner()

	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}
	c, err := NewCoordinator(j, tx, em, reg, clk, WithConfig(cfg))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(ctx) }()
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("coordinator not ready")
	}

	fakeclk := c.clock.(*clockmock.FakeClock)
	testwait.External(t, "tickers-registered",
		func() bool { return fakeclk.PendingTickers() >= 2 },
		testtime.D2s, testtime.D1ms)

	// Trigger a tick to claim the instance and start the blocking step.
	fakeclk.Advance(testtime.D10ms)

	// Wait for the step to have started (activeLeases non-empty).
	testwait.External(t, "step-inflight",
		func() bool {
			var n int
			c.activeLeases.Range(func(_, _ any) bool { n++; return true })
			return n > 0
		},
		testtime.D2s, testtime.D1ms)

	// Call Stop in a goroutine with a generous timeout.
	stopDone := make(chan error, 1)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer stopCancel()
	go func() {
		stopDone <- c.Stop(stopCtx)
	}()

	// Advance the fake clock continuously so the Stop drain ticker fires.
	go func() {
		for {
			select {
			case <-stopCtx.Done():
				return
			default:
				fakeclk.Advance(testtime.D10ms)
				time.Sleep(testtime.D1ms) //archtest:allow:test-sleep drain-ticker: advance fake clock for Stop drain
			}
		}
	}()

	// Unblock the step so it can complete.
	close(stepBlockCh)

	// Stop should return after the step finishes (drain detects activeLeases==0).
	select {
	case stopErr := <-stopDone:
		if stopErr != nil && !errors.Is(stopErr, context.Canceled) {
			t.Errorf("Stop returned error: %v", stopErr)
		}
	case <-time.After(testtime.D3s):
		t.Error("Stop did not return after step completed")
	}

	// Step must have finished.
	select {
	case <-stepDone:
	default:
		t.Error("step did not complete before Stop returned")
	}

	cancel()
	select {
	case <-startDone:
	case <-time.After(testtime.D3s):
		t.Error("coordinator goroutine did not exit")
	}
}

// TestStop_DrainTimeout verifies that Stop() returns a deadline-exceeded error
// when the stopCtx expires before all in-flight steps complete.
func TestStop_DrainTimeout(t *testing.T) {
	const defID idutil.SafeID = "stopdraintimeout"

	// stepBlockCh is never closed; step blocks until ctx is canceled.
	stepBlockCh := make(chan struct{})
	defer close(stepBlockCh) // cleanup; won't fire during test

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "neverendingstep",
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					select {
					case <-stepBlockCh:
						return []byte(`{}`), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
			},
		},
	}

	clk := newFakeClock()
	j := newMemJournal(clk)
	reg, regErr := ksaga.NewInMemoryRegistry(def)
	if regErr != nil {
		t.Fatalf("NewInMemoryRegistry: %v", regErr)
	}
	em := newSafeFakeEmitter()
	tx := newSafeFakeTxRunner()

	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}
	c, err := NewCoordinator(j, tx, em, reg, clk, WithConfig(cfg))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(ctx) }()
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("coordinator not ready")
	}

	fakeclk := c.clock.(*clockmock.FakeClock)
	testwait.External(t, "tickers-registered",
		func() bool { return fakeclk.PendingTickers() >= 2 },
		testtime.D2s, testtime.D1ms)

	// Trigger a tick to claim the instance.
	fakeclk.Advance(testtime.D10ms)

	// Wait for the step to be in-flight.
	testwait.External(t, "step-inflight",
		func() bool {
			var n int
			c.activeLeases.Range(func(_, _ any) bool { n++; return true })
			return n > 0
		},
		testtime.D2s, testtime.D1ms)

	// Call Stop with a very short stopCtx (expires before step finishes).
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D50ms)
	defer stopCancel()

	// Advance the fake clock so the Stop drain ticker can fire.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				fakeclk.Advance(testtime.D10ms)
				time.Sleep(testtime.D1ms) //archtest:allow:test-sleep drain-ticker: advance fake clock for Stop drain
			}
		}
	}()

	stopErr := c.Stop(stopCtx)

	// Stop should return with a deadline-exceeded error.
	if stopErr == nil {
		t.Error("Stop should return error when stopCtx expires before drain")
	}
	var ecErr *errcode.Error
	if !errors.As(stopErr, &ecErr) {
		t.Fatalf("expected *errcode.Error, got %T: %v", stopErr, stopErr)
	}
	if ecErr.Kind != errcode.KindDeadlineExceeded {
		t.Errorf("error kind = %v, want KindDeadlineExceeded", ecErr.Kind)
	}

	// The coordinator goroutine should still exit (cancel the outer ctx).
	cancel()
	select {
	case <-startDone:
	case <-time.After(testtime.D3s):
		t.Error("coordinator goroutine did not exit")
	}
}

// ---------------------------------------------------------------------------
// F11 — TestNewCoordinator_TypedNilJournal
// ---------------------------------------------------------------------------

// nilJournal is a concrete type that implements journal.Journal but whose
// pointer value is nil. Passed as journal.Journal interface, this is a
// typed-nil and must be caught by validation.IsNilInterface.
type nilJournal struct{ *journal.MemJournal }

func TestNewCoordinator_TypedNilJournal(t *testing.T) {
	clk := newFakeClock()
	tx := &fakeTxRunner{}
	em := &fakeEmitter{}
	reg := newRegistry()

	// Cast a (*nilJournal)(nil) to the journal.Journal interface — typed nil.
	var j journal.Journal = (*nilJournal)(nil)

	c, err := NewCoordinator(j, tx, em, reg, clk)
	if c != nil {
		t.Error("expected nil Coordinator on typed-nil journal")
	}
	if err == nil {
		t.Fatal("expected non-nil error for typed-nil journal")
	}
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ecErr.Kind != errcode.KindInvalid {
		t.Errorf("kind = %v, want KindInvalid", ecErr.Kind)
	}
}

// ---------------------------------------------------------------------------
// F4 — TestFailurePayload_NoInternalLeak
// ---------------------------------------------------------------------------

// TestFailurePayload_NoInternalLeak constructs an errcode.Error that carries
// WithInternal("secret=hunter2") and WithDetails(slog.String("password","p")),
// calls failurePayload, and asserts the JSON result contains only the const
// literal message — no secret or password substring.
func TestFailurePayload_NoInternalLeak(t *testing.T) {
	err := errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"step blew up",
		errcode.WithInternal("secret=hunter2"),
		errcode.WithDetails(slog.String("password", "p")),
	)

	payload := failurePayload(err)

	// Verify it is valid JSON with a "reason" key.
	var result struct {
		Reason string `json:"reason"`
	}
	if jsonErr := json.Unmarshal(payload, &result); jsonErr != nil {
		t.Fatalf("failurePayload produced invalid JSON: %v", jsonErr)
	}

	// The reason must be the const-literal message, not any runtime data.
	if result.Reason != "step blew up" {
		t.Errorf("reason = %q, want %q", result.Reason, "step blew up")
	}

	// Assert no PII leakage.
	payloadStr := string(payload)
	for _, forbidden := range []string{"secret", "hunter2", "password"} {
		if strings.Contains(payloadStr, forbidden) {
			t.Errorf("failurePayload contains forbidden string %q: %s", forbidden, payloadStr)
		}
	}
}

// ---------------------------------------------------------------------------
// F2 — TestDriveOne_StepDeadlineExceeded_MarkExpired
// ---------------------------------------------------------------------------

// TestDriveOne_StepDeadlineExceeded_MarkExpired verifies that when a step's
// context deadline fires (step.Timeout > 0), driveOne marks the instance
// Expired and does NOT append KindStepCompleted.
//
// Design: The coordinator uses c.clock.Now() to compute stepDeadline.
// Since FakeClock.Now() returns a time in 2024 (far in the past relative to
// real wall clock), context.WithDeadline(ctx, fakeNow+50ms) creates an
// immediately-expired context — the step receives ctx.Done() right away.
// The coordinator detects runCtx.Err() != nil and marks Expired.
func TestDriveOne_StepDeadlineExceeded_MarkExpired(t *testing.T) {
	const defID idutil.SafeID = "stepdeadlineexceeded"

	stepRunCount := 0
	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "timeoutstep",
				// step.Timeout = 50ms; since FakeClock time is far in the past,
				// the derived context deadline fires immediately — the step sees
				// ctx.Done() and returns ctx.Err().
				Timeout: testtime.D50ms,
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					stepRunCount++
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		},
	}

	clk := newFakeClock()
	j := newMemJournal(clk)
	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}
	em := &fakeEmitter{}
	tx := &fakeTxRunner{}

	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}
	c, err := NewCoordinator(j, tx, em, reg, clk, WithConfig(cfg))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(ctx) }()
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("coordinator not ready")
	}

	// Wait for tickers to register.
	fakeclk := c.clock.(*clockmock.FakeClock)
	testwait.External(t, "tickers-registered",
		func() bool { return fakeclk.PendingTickers() >= 2 },
		testtime.D2s, testtime.D1ms)

	// Trigger a tick to start the step.
	fakeclk.Advance(testtime.D10ms)

	// Wait for the instance to become terminal (KindSagaExpired).
	// The step deadline fires immediately (fake clock time is in the past),
	// so the coordinator quickly marks the instance Expired.
	testwait.External(t, "instance-expired",
		func() bool {
			evs, loadErr := j.Load(context.Background(), inst.ID)
			if loadErr != nil || len(evs) == 0 {
				return false
			}
			return evs[len(evs)-1].Kind == journal.KindSagaExpired
		},
		testtime.D2s, testtime.D2ms)

	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Assert final event is KindSagaExpired.
	if last := evs[len(evs)-1]; last.Kind != journal.KindSagaExpired {
		t.Errorf("last event = %s, want saga_expired", last.Kind)
	}

	// Assert NO KindStepCompleted was appended.
	for _, ev := range evs {
		if ev.Kind == journal.KindStepCompleted {
			t.Errorf("unexpected KindStepCompleted in journal after step timeout: %v", evs)
		}
	}

	// Assert no outbox entry was emitted (timeout path has no step-completed event).
	if len(em.entries) != 0 {
		t.Errorf("emitter entries = %d, want 0 on expired saga", len(em.entries))
	}

	// Step was called once (then returned via ctx.Done()).
	if stepRunCount != 1 {
		t.Errorf("stepRunCount = %d, want 1", stepRunCount)
	}

	// Stop coordinator.
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer stopCancel()
	if stopErr := c.Stop(stopCtx); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		t.Errorf("Stop: %v", stopErr)
	}
	select {
	case <-startDone:
	case <-time.After(testtime.D3s):
		t.Error("coordinator goroutine did not exit")
	}
}

// mustNewUUID is a test helper that creates a UUID or fatals.
func mustNewUUID(t *testing.T) idutil.SafeID {
	t.Helper()
	id, err := idutil.NewUUID()
	if err != nil {
		t.Fatalf("NewUUID: %v", err)
	}
	return idutil.SafeID(id)
}

// ---------------------------------------------------------------------------
// F8 — TestDriveOne_EmitFails_AppendRolledBack
// ---------------------------------------------------------------------------

// TestDriveOne_EmitFails_AppendRolledBack verifies real-PG atomicity: when
// outbox Emit fails inside the transaction (after Append has been called),
// the prior Append must NOT leave a KindStepCompleted event in the journal.
//
// The test uses stagedJournal + safeFakeTxRunner so the test fake mirrors
// the production PG tx-rollback semantic that plain MemJournal cannot
// exhibit. Without staging, this test would pass with a buggy coordinator
// (Append residue in the journal after a failed tx) — the bug F8 originally
// flagged as "false confidence".
func TestDriveOne_EmitFails_AppendRolledBack(t *testing.T) {
	const defID idutil.SafeID = "emitfailsatomic"

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{Name: "step1", Run: noopStep},
		},
	}

	clk := newFakeClock()
	memJ := newMemJournal(clk)
	staged := newStagedJournal(memJ)

	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}

	em := newSafeFakeEmitter()
	em.SetError(errors.New("emit intentionally failed"))

	tx := newSafeFakeTxRunner()

	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}

	c, err := NewCoordinator(staged, tx, em, reg, clk, WithConfig(cfg))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := memJ.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, _, claimErr := memJ.ClaimPending(context.Background(), 1, testtime.D60s)
	if claimErr != nil {
		t.Fatalf("ClaimPending: %v", claimErr)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claimed instance, got %d", len(claimed))
	}
	ci := claimed[0]

	driveErr := c.driveOne(context.Background(), ci)
	if driveErr == nil {
		t.Fatal("expected driveOne to return an error when Emit fails")
	}

	// Atomicity assertion: no KindStepCompleted event should have leaked.
	events, loadErr := memJ.Load(context.Background(), inst.ID)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	for _, ev := range events {
		if ev.Kind == journal.KindStepCompleted {
			t.Errorf("KindStepCompleted leaked into journal after Emit-failed tx: %+v (rollback broken)", ev)
		}
	}
}

// ---------------------------------------------------------------------------
// F8 — TestDriveOne_MarkTerminalFails_AppendRolledBack
// ---------------------------------------------------------------------------

// TestDriveOne_MarkTerminalFails_AppendRolledBack verifies that when
// MarkTerminal fails after Append+Emit inside the transaction, the prior
// Append is rolled back (no KindStepCompleted residue). Uses stagedJournal
// composed over failingFakeJournal so MarkTerminal fires the injected error
// while Append remains buffered until commit.
func TestDriveOne_MarkTerminalFails_AppendRolledBack(t *testing.T) {
	const defID idutil.SafeID = "marktermfailsatomic"

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			// Single step → isLastStep=true → commitStepCompleted calls MarkTerminal.
			{Name: "step1", Run: noopStep},
		},
	}

	clk := newFakeClock()
	memJ := newMemJournal(clk)
	failing := newFailingFakeJournal(memJ)
	failing.failMarkTerminalOnCall = 1
	staged := newStagedJournal(failing)

	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}
	em := newSafeFakeEmitter()
	tx := newSafeFakeTxRunner()

	cfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}

	c, err := NewCoordinator(staged, tx, em, reg, clk, WithConfig(cfg))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := memJ.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, _, claimErr := memJ.ClaimPending(context.Background(), 1, testtime.D60s)
	if claimErr != nil {
		t.Fatalf("ClaimPending: %v", claimErr)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claimed instance, got %d", len(claimed))
	}
	ci := claimed[0]

	driveErr := c.driveOne(context.Background(), ci)
	if driveErr == nil {
		t.Fatal("expected driveOne to return an error when MarkTerminal fails")
	}

	events, loadErr := memJ.Load(context.Background(), inst.ID)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	for _, ev := range events {
		if ev.Kind == journal.KindStepCompleted {
			t.Errorf("KindStepCompleted leaked into journal after MarkTerminal-failed tx: %+v (rollback broken)", ev)
		}
	}
}
