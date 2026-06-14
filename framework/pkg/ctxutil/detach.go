// Package ctxutil provides small context.Context helpers for crossing
// cancellation boundaries that the standard library does not directly model.
//
// Scope: critical-write paths (security cascade revoke, compensating cleanup,
// audit flush) where caller cancellation must NOT abort the write. dev/debug
// scenarios that just want "ignore cancel" should use stdlib
// context.WithoutCancel directly; this package layers a bounded timeout on
// top so detached writes cannot leak goroutines.
//
// ref: golang/go context.WithoutCancel proposal#40221
// ref: hashicorp/vault vault/token_store.go (quitContext detached pattern)
package ctxutil

import (
	"context"
	"time"
)

// WithDetachedTimeout returns a context that inherits all Values from parent
// (trace IDs, auth principal, request id) but is NOT canceled when parent is
// canceled, and carries its own absolute deadline.
//
// Use this for critical writes that must complete regardless of caller
// cancellation: cascade revoke on reuse detection, compensating cleanup,
// audit log flush. Do NOT use for happy-path reads/writes — caller
// cancellation usually carries useful "stop wasting work" signal.
//
// The returned cancel must always be called (typically via defer) to release
// the timer regardless of which way the context is finished.
//
// Implementation: context.WithoutCancel breaks the cancel chain (Done() is
// nil-channel, Err() is nil) while preserving Value lookup; WithTimeout adds
// a fresh deadline on top.
func WithDetachedTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
