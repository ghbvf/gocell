package saga

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
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
	if cfg.EmptyClaimBackoff != testtime.D200ms {
		t.Errorf("EmptyClaimBackoff = %v, want 200ms", cfg.EmptyClaimBackoff)
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
				EmptyClaimBackoff: testtime.D200ms,
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
				EmptyClaimBackoff: testtime.D200ms,
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
				EmptyClaimBackoff: testtime.D200ms,
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
				EmptyClaimBackoff: testtime.D200ms,
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
				EmptyClaimBackoff: testtime.D200ms,
			},
			wantErr: true,
		},
		{
			name: "zero EmptyClaimBackoff",
			cfg: Config{
				PollInterval:      testtime.D200ms,
				ClaimBatchSize:    16,
				LeaseDuration:     testtime.D30s,
				HeartbeatInterval: testtime.D10s,
				EmptyClaimBackoff: 0,
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
				EmptyClaimBackoff: testtime.D200ms,
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
				EmptyClaimBackoff: testtime.D200ms,
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
				EmptyClaimBackoff: testtime.D200ms,
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
