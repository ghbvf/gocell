package worker

import (
	"context"
	"sync/atomic"
)

// LazyWorker is a Worker whose concrete delegate is resolved at runtime (via Set)
// after it's already wired into a WorkerGroup. Used when a Worker is produced
// during Bootstrap.Run after WithWorkers already captured this placeholder.
//
// ref: cmd/core-bundle/main.go::lazyBootstrapWorker — the hoisted source-of-truth.
//
// Thread safety: Set (writer) and Start/Stop (readers) synchronise via atomic.Pointer.
// Semantics: nil delegate → Start/Stop are no-op success (preserves lazyBootstrapWorker
// contract where adminExists==true yields no cleaner).
type LazyWorker struct {
	ptr    atomic.Pointer[Worker]
	hasSet atomic.Bool // tracks first-Set CAS winner for Set() return value
}

// Lazy returns a fresh LazyWorker with nil delegate.
func Lazy() *LazyWorker { return &LazyWorker{} }

// Set atomically assigns the delegate Worker. First call wins: subsequent calls
// are ignored and return false. Passing nil is rejected and returns false.
// The single-producer bootstrap sink contract is preserved.
func (l *LazyWorker) Set(w Worker) (stored bool) {
	if w == nil {
		return false
	}
	if !l.hasSet.CompareAndSwap(false, true) {
		return false
	}
	l.ptr.Store(&w)
	return true
}

// Start delegates to the underlying Worker. Nil delegate → no-op success.
func (l *LazyWorker) Start(ctx context.Context) error {
	p := l.ptr.Load()
	if p == nil {
		return nil
	}
	return (*p).Start(ctx)
}

// Stop delegates to the underlying Worker. Nil delegate → no-op success.
func (l *LazyWorker) Stop(ctx context.Context) error {
	p := l.ptr.Load()
	if p == nil {
		return nil
	}
	return (*p).Stop(ctx)
}

// Compile-time assertion.
var _ Worker = (*LazyWorker)(nil)
