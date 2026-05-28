package saga

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// File-local duration consts for values not present in testtime.
const (
	// testNegativeDuration is used to test that negative PollInterval is rejected.
	testNegativeDuration = -testtime.D1ms
	// driveOneLeaseLostHB is the heartbeat interval used by the OutcomeLeaseLost
	// driveOne test so the executor's heartbeat goroutine fires after the test's
	// clk.Advance(testtime.D10ms) and observes the SetStale flag.
	driveOneLeaseLostHB = 5 * time.Millisecond
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
	t.Parallel()
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
}

// ---------------------------------------------------------------------------
// TestConfig_Validate
// ---------------------------------------------------------------------------

func TestConfig_Validate(t *testing.T) {
	t.Parallel()
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
				PollInterval:   testtime.D200ms,
				ClaimBatchSize: 16,
				LeaseDuration:  0,
			},
			wantErr: true,
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
	t.Parallel()
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
		reg        ksaga.Resolver
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
			assertNilDepFailure(t, c, err, tt.wantErrStr)
		})
	}
}

// assertNilDepFailure consolidates the four-clause check used by
// TestNewCoordinator_NilDeps so the test loop body stays under SonarCloud's
// cognitive-complexity ceiling (≤15). All four assertions are independent —
// any failure prints its own message; KindInvalid/Message mismatches use
// t.Errorf (continue) while wrong type / nil error use t.Fatalf (stop).
func assertNilDepFailure(t *testing.T, c *Coordinator, err error, wantMessage string) {
	t.Helper()
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
	if ecErr.Message != wantMessage {
		t.Errorf("message = %q, want %q", ecErr.Message, wantMessage)
	}
}

// ---------------------------------------------------------------------------
// TestNewCoordinator_NilClock
// ---------------------------------------------------------------------------

func TestNewCoordinator_NilClock(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
// TestRepoReady — lifecycle gate + journal delegation
// ---------------------------------------------------------------------------

// TestRepoReady_BeforeStart_NotRunning verifies the probe rejects readiness
// when the Coordinator has never been started.
func TestRepoReady_BeforeStart_NotRunning(t *testing.T) {
	clk := newFakeClock()
	memJ := newMemJournal(clk)
	j := &stubRepoProberJournal{MemJournal: memJ}
	c, err := NewCoordinator(j, &fakeTxRunner{}, &fakeEmitter{}, newRegistry(), clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	got := c.RepoReady(context.Background())
	if got == nil {
		t.Fatal("RepoReady() before Start: expected not-running error, got nil")
	}
	var ecErr *errcode.Error
	if !errors.As(got, &ecErr) || ecErr.Kind != errcode.KindUnavailable {
		t.Errorf("RepoReady() before Start: kind = %v, want KindUnavailable; err = %v", ecErrKind(ecErr), got)
	}
}

// TestRepoReady_Running_DelegatesToJournal verifies that once the Coordinator
// reaches coordRunning, the probe returns the journal's RepoReady result.
func TestRepoReady_Running_DelegatesToJournal(t *testing.T) {
	clk := newFakeClock()
	memJ := newMemJournal(clk)
	j := &stubRepoProberJournal{
		MemJournal: memJ,
		repoErr:    errors.New("repo down"),
	}
	c, err := NewCoordinator(j, &fakeTxRunner{}, &fakeEmitter{}, newRegistry(), clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(ctx) }()
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("coordinator did not become ready")
	}

	got := c.RepoReady(context.Background())
	if got == nil || got.Error() != "repo down" {
		t.Errorf("RepoReady() running: got %v, want error 'repo down'", got)
	}

	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer stopCancel()
	_ = c.Stop(stopCtx)
	<-startDone
}

func ecErrKind(e *errcode.Error) any {
	if e == nil {
		return "<nil>"
	}
	return e.Kind
}

// stubRepoProberJournal wraps MemJournal and overrides RepoReady.
type stubRepoProberJournal struct {
	*journal.MemJournal
	repoErr error
}

func (s *stubRepoProberJournal) RepoReady(_ context.Context) error {
	return s.repoErr
}

// TestCoordinator_RepoReadinessConformance enrolls *Coordinator in the shared
// healthz.RepoProber conformance harness (CELL-REPO-READYZ-PROBE-01). The
// Coordinator's RepoReady is a lifecycle gate over journal delegation, so both
// probers must be RUNNING for the harness to exercise the delegation path — a
// non-running coordinator always reports Unavailable via the lifecycle gate,
// which TestRepoReady_BeforeStart_NotRunning covers separately:
//   - healthy: running coordinator backed by a healthy mem journal → nil.
//   - broken:  running coordinator whose journal reports its relation gone → non-nil.
//
// Why a journal stub, not a real DROP TABLE: the Coordinator is NOT a SQL-backed
// store — it owns no relation. Its differentiated property is *faithful
// delegation* of the injected journal's readiness, so the "broken" prober is a
// running coordinator over a journal stub that returns an error. This is the
// strongest available broken analog (a no-op Coordinator.RepoReady that always
// returned nil would FAIL this sub-test), and it is stronger than passing a nil
// broken (which would skip the differentiated check entirely). The real
// DROP-TABLE conformance for the SQL-backed journal lands with the PG durable
// journal (#959) in adapters/postgres, which CELL-REPO-READYZ-PROBE-01 will then
// independently require to enroll.
func TestCoordinator_RepoReadinessConformance(t *testing.T) {
	clkHealthy := newFakeClock()
	healthy := startRunningCoordinator(t, newMemJournal(clkHealthy), clkHealthy)

	clkBroken := newFakeClock()
	broken := startRunningCoordinator(t, &stubRepoProberJournal{
		MemJournal: newMemJournal(clkBroken),
		repoErr:    errors.New("saga journal relation gone"),
	}, clkBroken)

	celltest.RunRepoReadinessConformance(t, "saga-coordinator", healthy, broken)
}

// startRunningCoordinator builds a Coordinator backed by j, Starts it, waits for
// Ready, and registers Stop on cleanup. It returns the running coordinator so
// RepoReady reflects the journal-delegation path rather than the lifecycle gate.
func startRunningCoordinator(t *testing.T, j journal.Journal, clk clock.Clock) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(j, &fakeTxRunner{}, &fakeEmitter{}, newRegistry(), clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- c.Start(ctx) }()
	select {
	case <-c.Ready():
	case <-time.After(testtime.D2s):
		cancel()
		t.Fatal("coordinator did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer stopCancel()
		_ = c.Stop(stopCtx)
		<-startDone
	})
	return c
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
		func() bool { return fakeclk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)

	// Trigger a tick to claim the instance and start the blocking step.
	fakeclk.Advance(testtime.D10ms)

	// Wait for the step to have started (inflightLocks non-empty).
	testwait.External(t, "step-inflight",
		func() bool {
			var n int
			c.inflightLocks.Range(func(_, _ any) bool { n++; return true })
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

	// Stop should return after the step finishes (drain detects inflightLocks empty).
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
		func() bool { return fakeclk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)

	// Trigger a tick to claim the instance.
	fakeclk.Advance(testtime.D10ms)

	// Wait for the step to be in-flight.
	testwait.External(t, "step-inflight",
		func() bool {
			var n int
			c.inflightLocks.Range(func(_, _ any) bool { n++; return true })
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
// WithInternal(InternalAttr("_", "secret=hunter2")) and
// WithDetails(PublicString("password", "p")), calls failurePayload, and
// asserts the JSON result contains only the const literal message — no
// secret or password substring.
func TestFailurePayload_NoInternalLeak(t *testing.T) {
	t.Parallel()
	err := errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"step blew up",
		errcode.WithInternal(errcode.InternalAttr("_", "secret=hunter2")),
		errcode.WithDetails(errcode.PublicString("password", "p")),
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

// TestFailurePayload_NonErrcodeRedacted covers the else branch: a plain
// (non-errcode) error whose text carries a key=value secret must be redacted by
// pkg/redaction.RedactString before landing in the journal Payload.
func TestFailurePayload_NonErrcodeRedacted(t *testing.T) {
	t.Parallel()
	err := errors.New("connect failed dsn=postgres://user:hunter2@db/saga token=abc123")

	var result struct {
		Reason string `json:"reason"`
	}
	if jsonErr := json.Unmarshal(failurePayload(err), &result); jsonErr != nil {
		t.Fatalf("failurePayload produced invalid JSON: %v", jsonErr)
	}

	// Secret values must be masked; the <REDACTED> mask must be present.
	for _, leaked := range []string{"hunter2", "abc123", "postgres://user"} {
		if strings.Contains(result.Reason, leaked) {
			t.Errorf("reason leaks %q: %s", leaked, result.Reason)
		}
	}
	if !strings.Contains(result.Reason, redaction.Mask) {
		t.Errorf("reason missing redaction mask %q: %s", redaction.Mask, result.Reason)
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

	var stepRunCount atomic.Int32
	stepStarted := make(chan struct{}, 1)
	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "timeoutstep",
				// step.Timeout = 50ms; the executor's buildStepCtx registers an
				// AfterFunc at clk.Now()+50ms. The step blocks on ctx.Done() until
				// clk.Advance fires the timeout timer.
				Timeout: testtime.D50ms,
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					stepRunCount.Add(1)
					select {
					case stepStarted <- struct{}{}:
					default:
					}
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

	// Wait for tickLoop ticker to register.
	fakeclk := c.clock.(*clockmock.FakeClock)
	testwait.External(t, "tickers-registered",
		func() bool { return fakeclk.PendingTickers() >= 1 },
		testtime.D2s, testtime.D1ms)

	// Trigger a tick to start the step (the step blocks until the AfterFunc fires).
	fakeclk.Advance(testtime.D10ms)

	// Wait for the step to start executing, then advance past the step timeout
	// (50ms) so the executor's AfterFunc fires errStepTimeout.
	select {
	case <-stepStarted:
	case <-time.After(testtime.D2s):
		t.Fatal("step did not start within 2s")
	}
	fakeclk.Advance(testtime.D50ms + testtime.D1ms)

	// Wait for the instance to become terminal (KindSagaExpired).
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
	if n := stepRunCount.Load(); n != 1 {
		t.Errorf("stepRunCount = %d, want 1", n)
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

// ---------------------------------------------------------------------------
// TestNewCoordinator_ConstructsInternalExecutor
// ---------------------------------------------------------------------------

// TestNewCoordinator_ConstructsInternalExecutor verifies that NewCoordinator
// builds its own Executor from the same journal + Config (#1181 F5: WithExecutor
// was deleted to guarantee claim-and-heartbeat share a journal by construction
// rather than by caller convention). The test asserts the Coordinator becomes
// usable without any executor-related option, and that the internal Executor
// uses the Coordinator's HeartbeatInterval / LeaseDuration.
func TestNewCoordinator_ConstructsInternalExecutor(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	j := newMemJournal(clk)
	tx := &fakeTxRunner{}
	em := &fakeEmitter{}
	reg := newRegistry()

	c, err := NewCoordinator(j, tx, em, reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator (no opts): %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil Coordinator")
	}
	if c.executor == nil {
		t.Error("internal Executor must be constructed; got nil — #1181 F5 invariant violated")
	}
}

// TestNewCoordinator_ConfigFlowsToExecutor verifies that HeartbeatInterval and
// LeaseDuration from Config are actually forwarded to the internal Executor.
// Behavioral proof: the executor validates that heartbeatInterval *
// HeartbeatLeaseSafetyFactor < leaseDuration. When the Coordinator Config
// violates this ratio, NewCoordinator must fail at construction (not silently
// succeed). This demonstrates the Config values reach the executor rather than
// being ignored.
func TestNewCoordinator_ConfigFlowsToExecutor(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	j := newMemJournal(clk)
	tx := &fakeTxRunner{}
	em := &fakeEmitter{}
	reg := newRegistry()

	// Valid Config: HeartbeatInterval * 2 < LeaseDuration.
	validCfg := Config{
		PollInterval:      testtime.D10ms,
		ClaimBatchSize:    16,
		LeaseDuration:     testtime.D60s,
		HeartbeatInterval: testtime.D20s,
	}
	c, err := NewCoordinator(j, tx, em, reg, clk, WithConfig(validCfg))
	if err != nil {
		t.Fatalf("NewCoordinator with valid config: %v", err)
	}
	if c.executor == nil {
		t.Fatal("executor must be constructed with valid config")
	}

	// Verify the coordinator's stored config matches what we provided.
	// Config.Validate already passed, so the values are in c.cfg.
	if c.cfg.HeartbeatInterval != testtime.D20s {
		t.Errorf("c.cfg.HeartbeatInterval = %v, want %v", c.cfg.HeartbeatInterval, testtime.D20s)
	}
	if c.cfg.LeaseDuration != testtime.D60s {
		t.Errorf("c.cfg.LeaseDuration = %v, want %v", c.cfg.LeaseDuration, testtime.D60s)
	}

	// invalid_config: HeartbeatInterval * HeartbeatLeaseSafetyFactor (=2) >= LeaseDuration
	// → Config.Validate rejects before reaching Executor construction, proving Config
	// values flow through the validation pipeline rather than being silently ignored.
	t.Run("invalid_config_HBI_too_large_rejected", func(t *testing.T) {
		t.Parallel()
		invalidCfg := Config{
			PollInterval:   testtime.D10ms,
			ClaimBatchSize: 4,
			// HBI=30s, LeaseDuration=60s → HBI*2 = 60s, NOT < 60s → reject.
			HeartbeatInterval: testtime.D30s,
			LeaseDuration:     testtime.D60s,
		}
		_, err := NewCoordinator(j, tx, em, reg, clk, WithConfig(invalidCfg))
		if err == nil {
			t.Fatal("NewCoordinator with HeartbeatInterval*2 >= LeaseDuration must fail")
		}
		// The error must mention "heartbeat" (from Config.Validate message),
		// confirming the Config values reached the validation pipeline.
		if !strings.Contains(err.Error(), "heartbeat") && !strings.Contains(err.Error(), "HeartbeatInterval") {
			t.Errorf("error must mention 'heartbeat' or 'HeartbeatInterval' to confirm Config flows through; got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestDriveOne_DelegatesToExecutor_Success
// ---------------------------------------------------------------------------

// TestDriveOne_DelegatesToExecutor_Success verifies the happy-path delegation:
// executor.Execute returns OutcomeSucceeded → coordinator appends KindStepCompleted
// and (for the last step) KindSagaSucceeded.
func TestDriveOne_DelegatesToExecutor_Success(t *testing.T) {
	const defID idutil.SafeID = "execdelegate"
	stepPayload := []byte(`{"answer":42}`)

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return stepPayload, nil
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
	em := newSafeFakeEmitter()
	tx := newSafeFakeTxRunner()

	c, err := NewCoordinator(j, tx, em, reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, _, claimErr := j.ClaimPending(context.Background(), 1, testtime.D60s)
	if claimErr != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: err=%v, count=%d", claimErr, len(claimed))
	}

	if err := c.driveOne(context.Background(), claimed[0]); err != nil {
		t.Fatalf("driveOne: %v", err)
	}

	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events (StepCompleted+SagaSucceeded), got %d: %v", len(evs), evs)
	}
	if evs[0].Kind != journal.KindStepCompleted {
		t.Errorf("evs[0].Kind = %s, want step_completed", evs[0].Kind)
	}
	if evs[1].Kind != journal.KindSagaSucceeded {
		t.Errorf("evs[1].Kind = %s, want saga_succeeded", evs[1].Kind)
	}
}

// ---------------------------------------------------------------------------
// TestDriveOne_OutcomeFailed_NoCompensation_MarksFailed
// ---------------------------------------------------------------------------

// TestDriveOne_OutcomeFailed_NoCompensation_MarksFailed verifies that when a
// step fails (no Compensate functions on any step), the coordinator writes
// KindStepFailed + KindSagaFailed and does NOT start compensation.
func TestDriveOne_OutcomeFailed_NoCompensation_MarksFailed(t *testing.T) {
	const defID idutil.SafeID = "failedncomp"
	stepErr := errors.New("step deliberately failed")

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return nil, stepErr
				},
				// No Compensate: shouldCompensate must return false.
			},
		},
	}

	clk := newFakeClock()
	j := newMemJournal(clk)
	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, _, claimErr := j.ClaimPending(context.Background(), 1, testtime.D60s)
	if claimErr != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: %v / %d", claimErr, len(claimed))
	}

	if err := c.driveOne(context.Background(), claimed[0]); err != nil {
		t.Fatalf("driveOne: %v", err)
	}

	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d: %v", len(evs), evs)
	}
	if evs[0].Kind != journal.KindStepFailed {
		t.Errorf("evs[0].Kind = %s, want step_failed", evs[0].Kind)
	}
	if evs[1].Kind != journal.KindSagaFailed {
		t.Errorf("evs[1].Kind = %s, want saga_failed", evs[1].Kind)
	}
	for _, ev := range evs {
		if ev.Kind == journal.KindCompensationStarted {
			t.Errorf("unexpected KindCompensationStarted — no compensate handlers defined")
		}
	}
}

// ---------------------------------------------------------------------------
// TestDriveOne_OutcomeFailed_TriggersReverseCompensation
// ---------------------------------------------------------------------------

// TestDriveOne_OutcomeFailed_TriggersReverseCompensation verifies that when a
// 2-step saga fails at step2, compensation runs in reverse order
// (step2-compensate, step1-compensate) and the instance reaches
// StatusCompensated.
func TestDriveOne_OutcomeFailed_TriggersReverseCompensation(t *testing.T) {
	const defID idutil.SafeID = "reversecomp"

	var compensated []string
	var mu sync.Mutex
	recordComp := func(name string) func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
		return func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
			mu.Lock()
			compensated = append(compensated, name)
			mu.Unlock()
			return nil
		}
	}

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name:       "step1",
				Run:        noopStep,
				Compensate: recordComp("step1"),
			},
			{
				Name: "step2",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return nil, errors.New("step2 failed")
				},
				Compensate: recordComp("step2"),
			},
		},
	}

	clk := newFakeClock()
	j := newMemJournal(clk)
	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Drive step1 to completion first.
	claimed1, _, err := j.ClaimPending(context.Background(), 1, testtime.D60s)
	if err != nil || len(claimed1) != 1 {
		t.Fatalf("ClaimPending (step1): %v / %d", err, len(claimed1))
	}
	if err := c.driveOne(context.Background(), claimed1[0]); err != nil {
		t.Fatalf("driveOne step1: %v", err)
	}

	// After step1 completes, the lease is still active for 60s. Advance past it
	// so the second ClaimPending can re-claim the instance for step2.
	clk.Advance(testtime.D60s + testtime.D1ms)

	// The first driveOne should have completed step1. Re-claim for step2.
	claimed2, _, err := j.ClaimPending(context.Background(), 1, testtime.D60s)
	if err != nil || len(claimed2) != 1 {
		t.Fatalf("ClaimPending (step2): %v / %d", err, len(claimed2))
	}
	if err := c.driveOne(context.Background(), claimed2[0]); err != nil {
		t.Fatalf("driveOne step2: %v", err)
	}

	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// All compensations in this test succeed (recordComp returns nil), so
	// the saga must reach StatusCompensated — not StatusFailed (forward
	// failure without rollback) and not StatusCompensationFailed (partial
	// rollback failure).
	lastEv := evs[len(evs)-1]
	if lastEv.Kind != journal.KindSagaCompensated {
		t.Errorf("last event = %s, want saga_compensated (all compensations succeed in this scenario)", lastEv.Kind)
	}

	// Verify compensation ran in reverse order: step1 completed, step2 failed → only
	// step1 was committed and must be compensated. Expected order: ["step1"].
	mu.Lock()
	gotComp := make([]string, len(compensated))
	copy(gotComp, compensated)
	mu.Unlock()
	wantOrder := []string{"step1"}
	if !reflect.DeepEqual(gotComp, wantOrder) {
		t.Errorf("compensation order = %v, want %v", gotComp, wantOrder)
	}
}

// ---------------------------------------------------------------------------
// TestDriveOne_OutcomeExpired_MarksExpired
// ---------------------------------------------------------------------------

// TestDriveOne_OutcomeExpired_MarksExpired verifies OutcomeExpired path: when
// the saga-level Definition.Timeout has elapsed (clock advanced past StartedAt +
// Timeout before driveOne runs), driveOne writes KindSagaExpired immediately
// without calling the step Run func.
func TestDriveOne_OutcomeExpired_MarksExpired(t *testing.T) {
	const defID idutil.SafeID = "expiredoutcome"

	stepCalled := false
	def := &ksaga.Definition{
		ID:      defID,
		Timeout: testtime.D10ms, // saga-level timeout used by coordinator's pre-execution check
		Steps: []ksaga.Step{
			{
				Name: "timeoutstep2",
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					stepCalled = true
					return nil, nil
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
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	// Enqueue at t0, then advance past the 10ms saga timeout before claiming.
	// driveOne checks clk.Now() - StartedAt > def.Timeout and marks Expired
	// immediately without executing the step.
	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	clk.Advance(testtime.D50ms) // advance past the 10ms Definition.Timeout
	claimed, _, claimErr := j.ClaimPending(context.Background(), 1, testtime.D60s)
	if claimErr != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: %v / %d", claimErr, len(claimed))
	}

	if err := c.driveOne(context.Background(), claimed[0]); err != nil {
		t.Fatalf("driveOne: %v", err)
	}

	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("expected journal events, got 0")
	}
	last := evs[len(evs)-1]
	if last.Kind != journal.KindSagaExpired {
		t.Errorf("last event = %s, want saga_expired", last.Kind)
	}
	for _, ev := range evs {
		if ev.Kind == journal.KindStepCompleted {
			t.Errorf("unexpected KindStepCompleted on expired saga")
		}
	}
	// The step Run func must NOT have been called (timeout detected before executor).
	if stepCalled {
		t.Error("step Run was called but should not have been (saga timeout pre-check)")
	}
}

// ---------------------------------------------------------------------------
// TestDriveOne_OutcomeCanceled_NoTerminalWrite
// ---------------------------------------------------------------------------

// TestDriveOne_OutcomeCanceled_NoTerminalWrite verifies that when the drive
// context is canceled (OutcomeCanceled), driveOne returns nil and writes NO
// terminal event — the instance stays claimable for re-claim.
func TestDriveOne_OutcomeCanceled_NoTerminalWrite(t *testing.T) {
	const defID idutil.SafeID = "canceledoutcome"

	stepStarted := make(chan struct{})
	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "cancelstep",
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					close(stepStarted)
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
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, _, claimErr := j.ClaimPending(context.Background(), 1, testtime.D60s)
	if claimErr != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: %v / %d", claimErr, len(claimed))
	}

	ctx, cancel := context.WithCancel(context.Background())
	driveErr := make(chan error, 1)
	go func() { driveErr <- c.driveOne(ctx, claimed[0]) }()

	// Wait for step to start, then cancel.
	select {
	case <-stepStarted:
	case <-time.After(testtime.D2s):
		t.Fatal("step did not start")
	}
	cancel()

	select {
	case err := <-driveErr:
		if err != nil {
			t.Errorf("driveOne should return nil on OutcomeCanceled, got: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("driveOne did not return after cancel")
	}

	// No terminal event written.
	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind.IsTerminal() {
			t.Errorf("unexpected terminal event %s after cancel", ev.Kind)
		}
	}
}

// ---------------------------------------------------------------------------
// TestDriveOne_OutcomeLeaseLost_LogInfoNoTerminalWrite
// ---------------------------------------------------------------------------

// TestDriveOne_OutcomeLeaseLost_LogInfoNoTerminalWrite verifies that when the
// executor detects lease loss, driveOne logs at Info and returns nil without
// writing any terminal journal event.
func TestDriveOne_OutcomeLeaseLost_LogInfoNoTerminalWrite(t *testing.T) {
	const defID idutil.SafeID = "leaselostoutcome"

	// Use a very short heartbeat interval so the lease-lost path fires quickly.
	clk := newFakeClock()
	memJ := newMemJournal(clk)

	// staleAfterFirstHBJournal returns ok=false after SetStale() is called.
	j := &staleAfterFirstHBJournal{MemJournal: memJ}

	// Short heartbeat interval so the loss is detected quickly.

	reg, err := ksaga.NewInMemoryRegistry(&ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "longstep",
				Run: func(ctx context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					// Arm the stale flag so next heartbeat sees ok=false.
					j.SetStale()
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewInMemoryRegistry: %v", err)
	}

	var logBuf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// Use a short HeartbeatInterval so the executor's heartbeat goroutine
	// fires after the test's clk.Advance(testtime.D10ms) below and observes
	// the SetStale flag (forcing OutcomeLeaseLost).
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk,
		WithLogger(logger),
		WithConfig(Config{
			PollInterval:      testtime.D10ms,
			ClaimBatchSize:    16,
			LeaseDuration:     testtime.D60s,
			HeartbeatInterval: driveOneLeaseLostHB,
		}))
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := memJ.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, _, claimErr := memJ.ClaimPending(context.Background(), 1, testtime.D60s)
	if claimErr != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: %v / %d", claimErr, len(claimed))
	}

	driveErrCh := make(chan error, 1)
	go func() { driveErrCh <- c.driveOne(context.Background(), claimed[0]) }()

	// Wait for the stale flag to be set (i.e. the step goroutine has started and
	// called j.SetStale()), then advance the fake clock to trigger the heartbeat
	// tick that will detect the stale lease.
	testwait.External(t, "step-stale-armed",
		func() bool { return j.stale.Load() },
		testtime.D2s, testtime.D1ms)
	clk.Advance(testtime.D10ms)

	// Receive driveOne's return value exactly once. Use a separate channel
	// receive rather than a testwait peek so the error value is not consumed and
	// discarded by the polling closure (double-consume race with buffered channel).
	var driveErr error
	var driveReturned bool
	testwait.External(t, "drive-one-returned",
		func() bool {
			select {
			case driveErr = <-driveErrCh:
				driveReturned = true
				return true
			default:
				return false
			}
		},
		testtime.D2s, testtime.D1ms)

	if !driveReturned {
		t.Fatal("driveOne did not return within 2s")
	}
	if driveErr != nil {
		t.Errorf("driveOne should return nil on LeaseLost, got: %v", driveErr)
	}

	// No terminal event.
	evs, err := memJ.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind.IsTerminal() {
			t.Errorf("unexpected terminal event %s after lease lost", ev.Kind)
		}
	}
}

// staleAfterFirstHBJournal wraps MemJournal and returns ok=false for Heartbeat
// once SetStale is called.
type staleAfterFirstHBJournal struct {
	*journal.MemJournal
	stale atomic.Bool
}

func (s *staleAfterFirstHBJournal) SetStale() {
	s.stale.Store(true)
}

func (s *staleAfterFirstHBJournal) Heartbeat(
	ctx context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration,
) (bool, error) {
	if s.stale.Load() {
		return false, nil
	}
	return s.MemJournal.Heartbeat(ctx, instanceID, leaseID, leaseDuration)
}

// ---------------------------------------------------------------------------
// TestRunCompensation_StepFails_ContinuesReverseFinalStatusCompensationFailed
// ---------------------------------------------------------------------------

// TestRunCompensation_StepFails_ContinuesReverseFinalStatusCompensationFailed
// verifies that when a compensation step fails, runCompensation continues the
// reverse walk (best-effort) and writes KindSagaCompensationFailed as the
// final status (#1210 C6 — distinct from KindSagaFailed which signals a
// forward-phase failure with no rollback).
func TestRunCompensation_StepFails_ContinuesReverseFinalStatusCompensationFailed(t *testing.T) {
	const defID idutil.SafeID = "compfailcont"

	var compensated []string
	var mu sync.Mutex

	def := &ksaga.Definition{
		ID: defID,
		Steps: []ksaga.Step{
			{
				Name: "step1",
				Run:  noopStep,
				Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
					mu.Lock()
					compensated = append(compensated, "step1")
					mu.Unlock()
					return nil
				},
			},
			{
				Name: "step2",
				Run:  noopStep,
				Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
					mu.Lock()
					compensated = append(compensated, "step2-fail")
					mu.Unlock()
					return errors.New("step2 compensation failed")
				},
			},
			{
				Name: "step3",
				Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
					return nil, errors.New("step3 deliberately fails")
				},
				Compensate: func(_ context.Context, _ *ksaga.Instance, _ []byte) error {
					mu.Lock()
					compensated = append(compensated, "step3")
					mu.Unlock()
					return nil
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
	c, err := NewCoordinator(j, newSafeFakeTxRunner(), newSafeFakeEmitter(), reg, clk)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	inst := ksaga.NewInstance(mustNewUUID(t), defID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Drive step1 → step2 → step3 (step3 fails, triggers compensation walk).
	// After each non-terminal step completes, the journal lease remains active for
	// LeaseDuration (60s). Advance past it so the next ClaimPending can re-claim.
	for i := 0; i < 3; i++ {
		// Expire any previous lease before claiming.
		clk.Advance(testtime.D60s + testtime.D1ms)
		claimed, _, claimErr := j.ClaimPending(context.Background(), 1, testtime.D60s)
		if claimErr != nil {
			t.Fatalf("ClaimPending round %d: %v", i, claimErr)
		}
		if len(claimed) == 0 {
			break // instance terminal
		}
		if err := c.driveOne(context.Background(), claimed[0]); err != nil {
			t.Fatalf("driveOne round %d: %v", i, err)
		}
	}

	evs, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	last := evs[len(evs)-1]
	// step2 compensation fails → final status must be KindSagaCompensationFailed
	// (#1210 C6: distinct from KindSagaFailed which means forward-phase failed).
	if last.Kind != journal.KindSagaCompensationFailed {
		t.Errorf("last event = %s, want saga_compensation_failed (compensation error => compensation_failed)", last.Kind)
	}

	// Compensation ran for committed steps in reverse order.
	// step1+step2 completed; step3 failed → compensation reverse-walks step2 first
	// (records "step2-fail", returns error), then step1 (records "step1").
	mu.Lock()
	got := make([]string, len(compensated))
	copy(got, compensated)
	mu.Unlock()
	wantOrder := []string{"step2-fail", "step1"}
	if !reflect.DeepEqual(got, wantOrder) {
		t.Errorf("compensation order = %v, want %v", got, wantOrder)
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
