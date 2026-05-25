package saga

// testfakes_test.go provides additional fake helpers for the integration test
// suite in integration_test.go. All types are in package saga (white-box).
//
// NOTE: coordinator_test.go (same package) already declares:
//   - fakeEmitter  (no mu / no Snapshot — single goroutine only)
//   - fakeTxRunner (with err field for error injection)
//   - noopStep, newFakeClock, newMemJournal, newRegistry, mustCoordinator
//
// This file declares supplementary helpers used by the integration tests.

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// ---------------------------------------------------------------------------
// safeFakeEmitter — thread-safe emitter with Snapshot (for concurrent tests)
// ---------------------------------------------------------------------------

// safeFakeEmitter captures outbox entries in a thread-safe slice. It is
// separate from the single-goroutine fakeEmitter already declared in
// coordinator_test.go and is used wherever the Coordinator goroutine and the
// test goroutine race on the entries slice.
type safeFakeEmitter struct {
	mu      sync.Mutex
	entries []koutbox.Entry
	err     error // if non-nil, Emit returns this error (sticky until cleared)
}

func newSafeFakeEmitter() *safeFakeEmitter {
	return &safeFakeEmitter{}
}

func (e *safeFakeEmitter) Emit(_ context.Context, entry koutbox.Entry) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	e.entries = append(e.entries, entry)
	return nil
}

// Snapshot returns a defensive copy of captured entries.
func (e *safeFakeEmitter) Snapshot() []koutbox.Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]koutbox.Entry, len(e.entries))
	copy(out, e.entries)
	return out
}

// Count returns the number of entries captured so far.
func (e *safeFakeEmitter) Count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.entries)
}

// SetError configures the emitter to return err on the next Emit call
// (sticky until ClearError is called).
func (e *safeFakeEmitter) SetError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.err = err
}

// ClearError clears a previously set error.
func (e *safeFakeEmitter) ClearError() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.err = nil
}

// ---------------------------------------------------------------------------
// safeFakeTxRunner — after-commit aware TxRunner (mirrors
// persistencetest/conformance.go pattern) for integration tests that need
// concurrent-safe after-commit hook draining.
// ---------------------------------------------------------------------------

// safeFakeTxRunner is a TxRunner that installs an AfterCommit registry, runs
// fn, drains hooks on success, and truncates hooks on failure. It mirrors the
// persistencetest/conformance.go pattern. Thread-safe.
type safeFakeTxRunner struct{}

func newSafeFakeTxRunner() *safeFakeTxRunner { return &safeFakeTxRunner{} }

func (f *safeFakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	txCtx, installed := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(txCtx)

	if err := fn(txCtx); err != nil {
		persistence.TruncateAfterCommitTo(txCtx, mark)
		return err
	}
	if installed {
		persistence.RunAfterCommitHooks(txCtx)
	}
	return nil
}

// ---------------------------------------------------------------------------
// failingFakeJournal — wraps a Journal and injects errors on demand
// ---------------------------------------------------------------------------

// failingFakeJournal wraps an inner journal.Journal and can inject errors at
// configurable call numbers. A value of 0 for a counter means "never fail".
// Used by atomicity tests to verify error propagation behavior.
type failingFakeJournal struct {
	inner journal.Journal

	mu sync.Mutex

	// failAppendOnCall: if > 0, Append returns an injected error on that call number.
	failAppendOnCall int
	appendCalls      int

	// failMarkTerminalOnCall: if > 0, MarkTerminal returns an injected error on
	// that call number.
	failMarkTerminalOnCall int
	markTermCalls          int
}

func newFailingFakeJournal(inner journal.Journal) *failingFakeJournal {
	return &failingFakeJournal{inner: inner}
}

func (f *failingFakeJournal) Enqueue(ctx context.Context, instance ksaga.Instance) error {
	return f.inner.Enqueue(ctx, instance)
}

func (f *failingFakeJournal) Append(ctx context.Context, instanceID, leaseID idutil.SafeID, event journal.Event) (int64, error) {
	f.mu.Lock()
	f.appendCalls++
	n := f.appendCalls
	fail := f.failAppendOnCall
	f.mu.Unlock()

	if fail > 0 && n == fail {
		return 0, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"saga: failingFakeJournal injected Append error")
	}
	return f.inner.Append(ctx, instanceID, leaseID, event)
}

func (f *failingFakeJournal) MarkTerminal(ctx context.Context, instanceID, leaseID idutil.SafeID, finalStatus ksaga.Status) (bool, error) {
	f.mu.Lock()
	f.markTermCalls++
	n := f.markTermCalls
	fail := f.failMarkTerminalOnCall
	f.mu.Unlock()

	if fail > 0 && n == fail {
		return false, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"saga: failingFakeJournal injected MarkTerminal error")
	}
	return f.inner.MarkTerminal(ctx, instanceID, leaseID, finalStatus)
}

func (f *failingFakeJournal) Load(ctx context.Context, instanceID idutil.SafeID) ([]journal.Event, error) {
	return f.inner.Load(ctx, instanceID)
}

func (f *failingFakeJournal) ClaimPending(
	ctx context.Context, batchSize int, leaseDuration time.Duration,
) ([]journal.ClaimedInstance, idutil.SafeID, error) {
	return f.inner.ClaimPending(ctx, batchSize, leaseDuration)
}

func (f *failingFakeJournal) Heartbeat(ctx context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error) {
	return f.inner.Heartbeat(ctx, instanceID, leaseID, leaseDuration)
}

func (f *failingFakeJournal) RepoReady(ctx context.Context) error {
	return f.inner.RepoReady(ctx)
}

// AppendCallCount returns the total number of Append calls so far.
func (f *failingFakeJournal) AppendCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appendCalls
}

// ---------------------------------------------------------------------------
// recordingDispatcher — counts Kick calls
// ---------------------------------------------------------------------------

// recordingDispatcher counts Kick invocations for assertion in integration
// tests. Separate from the saga_test-package version in dispatcher_test.go
// (which is inaccessible from package saga).
type recordingDispatcher struct {
	kicks atomic.Int32
}

func (r *recordingDispatcher) Kick(_ context.Context) {
	r.kicks.Add(1)
}

func (r *recordingDispatcher) KickCount() int {
	return int(r.kicks.Load())
}
