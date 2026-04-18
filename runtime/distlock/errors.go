package distlock

import "github.com/ghbvf/gocell/pkg/errcode"

// Error codes for distributed lock operations. Values are stable string IDs
// consumed by client-side error taxonomies and metrics label sets; they must
// not change without a coordinated rollout.
//
// The string values use noun form (ERR_DISTLOCK_ACQUIRE) rather than past
// tense (ERR_DISTLOCK_ACQUIRED) to match the convention established by
// pkg/errcode — which fixes the pre-existing value typo in the
// ERR_ADAPTER_REDIS_LOCK_ACQUIRED string that the old
// adapters/redis/client.go ErrAdapterRedisLockAcquire constant carried.
const (
	ErrLockAcquire errcode.Code = "ERR_DISTLOCK_ACQUIRE" // acquire I/O failure
	ErrLockRelease errcode.Code = "ERR_DISTLOCK_RELEASE" // release I/O failure
	ErrLockTimeout errcode.Code = "ERR_DISTLOCK_TIMEOUT" // another holder owns the key

	// ErrLockLost indicates the caller no longer owns the lock at the point of
	// Release. Covers every case where the Release call finds the lock is gone:
	//   - The lock TTL expired before Release was issued (implementation detects
	//     this locally via expiresAt and skips DEL; returns ErrLockLost).
	//   - The Redis Lua release script found a non-matching value (another
	//     holder took over, or our TTL expired between the script's GET and DEL).
	//   - Release was called twice on the same Lock (second call finds the key
	//     gone from the first).
	//
	// Callers can errors.Is / errors.As on this code to branch on loss semantics.
	// The collapsed Lost() channel (see runtime/distlock.Lock doc) also closes
	// in all these cases, giving callers two independent views on the same event
	// (channel-for-select, error-for-branch).
	ErrLockLost errcode.Code = "ERR_DISTLOCK_LOST"
)
