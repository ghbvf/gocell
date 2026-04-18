package outbox

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// FailureBudget tracks consecutive failures for a named operation and exposes
// a health.Checker compatible probe. When the consecutive failure count reaches
// the threshold the budget is "tripped" and Checker() returns a non-nil error.
// A single success resets the counter and clears the tripped state.
//
// Design: absolute count (no sliding window), success clears zero — mirrors
// K8s workqueue ItemExponentialFailureRateLimiter.Forget(item) semantics.
//
// ref: k8s.io/client-go/util/workqueue/default_rate_limiters.go — absolute
// count + Forget(item) clears to zero; no decay window.
// ref: controller-runtime/pkg/healthz — AddReadyzCheck(name, Checker) aggregation.
type FailureBudget struct {
	name      string
	threshold int64
	consec    atomic.Int64
	tripped   atomic.Bool
	// logged* tracks whether the log for the current state has been emitted
	// to prevent repeat log spam on stable tripped/recovered state.
	loggedTrip    atomic.Bool
	loggedRecover atomic.Bool
	logger        *slog.Logger
}

// NewFailureBudget creates a FailureBudget with the given name and threshold.
// threshold=0 disables the budget (Checker always returns nil, Tripped always false).
// Uses slog.Default() as the logger.
func NewFailureBudget(name string, threshold int) *FailureBudget {
	return NewFailureBudgetWithLogger(name, threshold, slog.Default())
}

// NewFailureBudgetWithLogger creates a FailureBudget with an explicit logger.
// This variant is used in tests to capture log output.
func NewFailureBudgetWithLogger(name string, threshold int, logger *slog.Logger) *FailureBudget {
	if logger == nil {
		logger = slog.Default()
	}
	return &FailureBudget{
		name:      name,
		threshold: int64(threshold),
		logger:    logger,
	}
}

// Record records the outcome of one operation. err!=nil increments the
// consecutive failure counter and trips the budget when the threshold is
// reached. err==nil resets the counter and clears the tripped state.
//
// Thread-safe: uses atomic operations throughout.
func (b *FailureBudget) Record(err error) {
	if b.threshold <= 0 {
		return // disabled
	}

	if err != nil {
		b.recordFailure()
	} else {
		b.recordSuccess()
	}
}

// recordFailure handles the err!=nil path of Record.
//
// Invariant: each state transition clears only the *opposite* side's log flag.
// The trip path (false→true) clears loggedRecover so the next recovery logs.
// The recover path (true→false) clears loggedTrip so the next trip logs.
// This prevents the ABA window where clearing the same-side flag could race
// with a concurrent recordSuccess that just set it — each CAS winner owns
// exactly one side and only resets the other.
func (b *FailureBudget) recordFailure() {
	newConsec := b.consec.Add(1)
	if newConsec < b.threshold {
		return
	}
	// At or past threshold: try to trip.
	if b.tripped.CompareAndSwap(false, true) {
		// We tripped: clear the recover-log flag so the next recovery logs.
		// Do NOT touch loggedTrip here — the CAS already guarantees we are the
		// sole goroutine entering this branch; loggedTrip is cleared by the
		// preceding recover path.
		b.loggedRecover.Store(false)
		if b.loggedTrip.CompareAndSwap(false, true) {
			b.logger.Warn("outbox relay: failure budget exhausted",
				slog.String("name", b.name),
				slog.Int64("threshold", b.threshold),
				slog.Int64("consecutive_failures", newConsec),
			)
		}
	}
}

// recordSuccess handles the err==nil path of Record.
//
// Invariant: see recordFailure. The recover path (true→false) clears
// loggedTrip so the next exhaustion logs. It does NOT touch loggedRecover,
// which is cleared by the trip path, eliminating the ABA window.
func (b *FailureBudget) recordSuccess() {
	b.consec.Store(0)
	if b.tripped.CompareAndSwap(true, false) {
		// We recovered: clear the trip-log flag so the next exhaustion logs.
		b.loggedTrip.Store(false)
		if b.loggedRecover.CompareAndSwap(false, true) {
			b.logger.Info("outbox relay: failure budget recovered",
				slog.String("name", b.name),
			)
		}
	}
}

// Checker returns a func() error suitable for use as a health.Checker.
// When threshold is 0 (disabled), returns nil.
// When the budget is not tripped, the returned func returns nil.
// When tripped, the returned func returns an error containing the budget name
// and threshold.
func (b *FailureBudget) Checker() func() error {
	if b.threshold <= 0 {
		return nil
	}
	return func() error {
		if !b.tripped.Load() {
			return nil
		}
		return errcode.New(errcode.ErrRelayBudgetExhausted,
			fmt.Sprintf("relay failure budget %q exhausted: %d consecutive failures reached threshold %d",
				b.name, b.consec.Load(), b.threshold))
	}
}

// Tripped returns true when the consecutive failure count has reached the
// threshold and has not yet been reset by a successful Record(nil) call.
func (b *FailureBudget) Tripped() bool {
	if b.threshold <= 0 {
		return false
	}
	return b.tripped.Load()
}

// ConsecutiveFailures returns the current consecutive failure count.
func (b *FailureBudget) ConsecutiveFailures() int64 {
	return b.consec.Load()
}
