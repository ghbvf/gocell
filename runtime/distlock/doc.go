// Package distlock defines the provider-neutral distributed-lock contract for
// the GoCell runtime layer. Concrete backend implementations live in adapters/
// (currently only adapters/redis).
//
// # Design rationale
//
// GoCell's layering rule prohibits runtime/ from importing adapters/, so the
// Locker / Lock interfaces must live here rather than in adapters/redis.
// The shape follows PR#177's runtime/outbox.Store precedent exactly.
//
// # Resource model
//
// Each call to New() creates one Manager. The Manager's resource footprint per
// active lock set is:
//   - 1 manager goroutine: owns the renewal min-heap and all Driver I/O calls
//   - 0 per-lock goroutines: lockCtx is derived from the caller's ctx, so parent
//     cancellation (including custom causes set via context.WithCancelCause),
//     values, and deadlines propagate automatically via Go's context machinery.
//     No watcher goroutine is needed.
//
// N active locks = 1 manager goroutine + O(N) heap. One goroutine for N locks.
//
// # Non-goals
//
// This is an efficiency lock, NOT a correctness lock. It is suitable for
// avoiding duplicate work (e.g., "only one pod runs a scheduled job").
// For correctness-critical paths use application-level conditional writes
// (e.g., Postgres optimistic locking with row versions). This matches the
// Redlock paper's own scoping: Redsync / redis/v9 make the same disclaimer.
//
// # References
//
//   - ref: github.com/go-redsync/redsync mutex.go — Lock/Unlock/Extend shape
//     rejected because GoCell auto-renews; Extend on the contract is backend-specific
//   - ref: github.com/etcd-io/etcd client/v3/concurrency/mutex.go — CAS-storage
//     shape, not adopted (over-specified for the GoCell use case)
//   - ref: github.com/hashicorp/consul/api lock.go — lostCh pattern adopted as Lost()
//   - ref: github.com/temporalio/sdk-go internal/internal_worker.go — stopC signal-by-close idiom
//   - ref: PR#177 runtime/outbox.Store — identical layering rationale
package distlock
