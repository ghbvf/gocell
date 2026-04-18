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

	// ErrLockLost identifies a lost lock (renewal failed or ownership taken by
	// another holder). The redis impl signals loss via Lock.Lost() channel close,
	// not an error return, so this code is not produced by current adapters.
	// Reserved for future impls that prefer error-return over channel signalling
	// (e.g. a synchronous session-revocation backend) and for consumers that want
	// a stable taxonomy entry to assert on in metrics labels.
	ErrLockLost errcode.Code = "ERR_DISTLOCK_LOST"
)
