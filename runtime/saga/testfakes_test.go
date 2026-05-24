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

	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
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
}

func newSafeFakeEmitter() *safeFakeEmitter {
	return &safeFakeEmitter{}
}

func (e *safeFakeEmitter) Emit(_ context.Context, entry koutbox.Entry) error {
	e.mu.Lock()
	defer e.mu.Unlock()
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
