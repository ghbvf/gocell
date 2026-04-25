package httputil

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/pkg/ctxcancel"
)

// cancelReasonKey is an unexported context key for the writable cancel-reason
// slot; using a struct{} (not a string) prevents collisions with other ctx
// keys defined elsewhere.
type cancelReasonKey struct{}

// cancelReasonSlot is a request-scoped, mutable holder for the 499 reason
// label. It is initialised by tracing middleware at request start, populated
// by writeErrcodeError when an ErrClientCanceled response is emitted, and
// read by tracing middleware after the handler returns to stamp the span's
// client.cancel.reason attribute.
//
// The mutex makes concurrent writes safe in case a request fans out to
// multiple goroutines that each emit a 499 response (rare, but the cost of
// a single sync.Mutex is negligible compared to the safety guarantee).
type cancelReasonSlot struct {
	mu     sync.Mutex
	reason string
}

// WithCancelReasonSlot returns a new context carrying a writable slot for the
// 499 client-cancel reason ("canceled" vs "deadline_exceeded"). Tracing
// middleware MUST install the slot before invoking the handler chain; without
// it, setCancelReason is a no-op and CancelReason returns the empty string,
// causing tracing to fall back to the legacy "context_canceled" label.
func WithCancelReasonSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, cancelReasonKey{}, &cancelReasonSlot{})
}

// CancelReason returns the 499 reason recorded for the current request, or
// the empty string when no slot was installed / no reason was set. Tracing
// middleware reads this after handler return to stamp the span attribute.
func CancelReason(ctx context.Context) string {
	slot, ok := ctx.Value(cancelReasonKey{}).(*cancelReasonSlot)
	if !ok {
		return ""
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	return slot.reason
}

// setCancelReason records a 499 reason on the slot installed in ctx (no-op
// when no slot is present). Called from writeErrcodeError when emitting an
// ErrClientCanceled response.
//
// Defence-in-depth: this slot setter enforces the same low-cardinality enum
// whitelist (ctxcancel.ReasonCanceled / ReasonDeadlineExceeded) as
// ctxcancel.ReasonFromDetails. writeErrcodeError already filters at the
// Details read site, so the inner guard is mostly belt-and-suspenders — but
// guarantees the slot contract holds even if a future caller bypasses
// ReasonFromDetails (e.g. by reading the value out of a header). Unknown
// values fall through to the legacy "context_canceled" tracing fallback,
// which dashboards already recognize as "instrumentation gap, fix the
// upstream".
func setCancelReason(ctx context.Context, reason string) {
	switch reason {
	case ctxcancel.ReasonCanceled, ctxcancel.ReasonDeadlineExceeded:
		// accepted — fall through to slot write
	default:
		return
	}
	slot, ok := ctx.Value(cancelReasonKey{}).(*cancelReasonSlot)
	if !ok {
		return
	}
	slot.mu.Lock()
	slot.reason = reason
	slot.mu.Unlock()
}
