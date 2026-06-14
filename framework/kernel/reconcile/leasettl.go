package reconcile

import (
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// LeaseTTL is a validated lease-window value — a sealed value type whose only
// constructor, NewLeaseTTL, rejects sub-millisecond durations so the truncation-to-
// zero defect is UNREPRESENTABLE for any constructed value rather than guarded at
// each adapter call site.
//
// The lease window reaches the Redis PX / Postgres `interval '1 millisecond'` lease
// commands as an integer-millisecond quantity (Milliseconds). A time.Duration finer
// than that wire unit truncates to 0 there, yielding a lease that expires the instant
// it is written — so a follower acquires immediately and multiple replicas run as
// leader at once, breaking the cross-replica mutual exclusion the monotonic
// LeaseToken.Epoch fencing relies on. NewLeaseTTL fail-closes that window at the
// boundary; no elector can be built with a sub-millisecond lease.
//
// Sealed construction: the unexported ms field means no package outside reconcile
// can mint a non-zero LeaseTTL via a struct literal; the only escape is the zero
// value LeaseTTL{} (ms == 0), which IsZero reports so elector constructors reject it
// fail-fast — a loud forgot-to-construct error, never the silent sub-ms truncation.
//
// INVARIANT: any LeaseTTL returned by NewLeaseTTL has ms >= 1, so Milliseconds()
// never truncates to 0 and Duration() is never sub-millisecond. Enforced by the
// unexported field (compile-time sealing) + NewLeaseTTL's guard; covered by
// leasettl_test.go. ref: kubernetes/client-go tools/leaderelection (LeaseDuration is
// a positive, usable window validated before election starts).
type LeaseTTL struct {
	ms int64
}

// NewLeaseTTL validates d as a lease window and returns its integer-millisecond
// form. d must be at least time.Millisecond — the wire granularity of the downstream
// Redis PX / Postgres interval lease commands; a sub-millisecond d is rejected
// fail-closed because Milliseconds() would truncate it to 0 and produce an
// instantly-expiring lease (multiple live leaders). A supra-millisecond d is floored
// to millisecond granularity, the honest resolution of the wire commands.
func NewLeaseTTL(d time.Duration) (LeaseTTL, error) {
	if d < time.Millisecond {
		// ErrCellInvalidConfig (not the generic ErrInternal): a sub-ms lease is a
		// wiring/config mistake, routable as such by operators. Kind stays Internal
		// — this fails at boot via composition-root fail-fast, never a user 4xx.
		return LeaseTTL{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"reconcile: lease TTL must be at least 1ms")
	}
	return LeaseTTL{ms: d.Milliseconds()}, nil
}

// Milliseconds returns the lease window in integer milliseconds for the Redis PX /
// Postgres interval argument. Always >= 1 for a LeaseTTL built by NewLeaseTTL.
func (t LeaseTTL) Milliseconds() int64 { return t.ms }

// Duration returns the lease window as a time.Duration (used to stamp
// LeaseToken.ExpiresAt). At millisecond granularity; always >= 1ms for a constructed
// value.
func (t LeaseTTL) Duration() time.Duration { return time.Duration(t.ms) * time.Millisecond }

// IsZero reports whether this is the unconstructed zero value (ms == 0). A non-zero
// LeaseTTL can only come from NewLeaseTTL, which guarantees ms >= 1; elector
// constructors reject the zero value fail-fast.
func (t LeaseTTL) IsZero() bool { return t.ms == 0 }
