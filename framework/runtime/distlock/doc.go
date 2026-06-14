// Package distlock defines the provider-neutral distributed-lock contract for
// the GoCell runtime layer. Concrete backend implementations live in adapters/
// (currently only adapters/redis).
//
// # Design rationale
//
// GoCell's layering rule prohibits runtime/ from importing adapters/, so the
// Locker / Driver interfaces must live here rather than in adapters/redis.
// The shape follows PR#177's runtime/outbox.Store precedent exactly.
//
// # Lock-as-Resource contract (see Lock type godoc)
//
// Locker.Acquire returns a *Lock, NOT a context.Context. Caller-ctx is
// consumed for the acquire RPC only; once held, the lock lifecycle is
// independent of caller ctx and ends only via Release(), Orphan(), renewal
// failure, or TTL expiry after Orphan(). This matches the prevailing industry convention
// (bsm/redislock, go-redsync, etcd, consul, Curator) and prevents the
// misuse class identified in GH #20.
//
// Three per-lock terminal signals (Lock.Cause()):
//   - ErrLockReleased — Release() was called: renewal stopped, backend key deleted.
//   - ErrLockOrphaned — Orphan() was called: renewal stopped, backend key NOT
//     deleted (expires on its lease TTL — ~1×TTL from the last successful
//     renewal; see Lock.Orphan godoc for the best-effort bound). Use for
//     graceful shutdown/handoff.
//     ref: etcd-io/etcd client/v3/concurrency/session.go Session.Orphan
//   - ErrLockLost     — renewal failed or ownership taken by another holder.
//
// # Resource model
//
// Each call to New() creates one Manager. The Manager's resource footprint per
// active lock set is:
//   - 1 manager goroutine: owns the renewal min-heap and dispatches every
//     Driver I/O call (Renew, Release) to a short-lived background goroutine so
//     the loop never blocks on a slow or unreachable backend — Orphan() / Stop()
//     stay responsive even while a Renew/Release RPC is in flight.
//   - 0 persistent per-lock goroutines: *Lock is a value handle; lock-end is
//     delivered by the manager via markCause (closes Done() channel, sets
//     Cause()). Transient goroutines exist only for the duration of an
//     individual Renew/Release RPC.
//
// N active locks = 1 manager goroutine + O(N) heap + transient per-RPC
// goroutines. One persistent goroutine for N locks.
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
//   - ref: github.com/go-redsync/redsync mutex.go — caller-ctx scoped to acquire
//   - ref: github.com/etcd-io/etcd client/v3/concurrency/session.go — session-scoped keepalive
//   - ref: github.com/hashicorp/consul/api lock.go — explicit Unlock contract
//   - ref: github.com/bsm/redislock — refresh as application concern
//   - ref: PR#177 runtime/outbox.Store — identical layering rationale
//   - ref: ADR docs/architecture/202605200000-adr-distlock-lock-as-resource.md
package distlock
