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

	// ErrLockLost identifies a lost lock. Returned by Release when the Lua
	// script finds result==0 — meaning the key is no longer owned by this
	// holder (TTL expired before our DEL, another holder took over, or Release
	// was called twice). Callers can errors.Is/errors.As on this code to branch
	// on loss semantics. The redis impl also signals loss via Lock.Lost()
	// channel close when renewal fails; both paths share this code so consumers
	// have a single stable taxonomy entry for metrics labels and alerting.
	ErrLockLost errcode.Code = "ERR_DISTLOCK_LOST"
)
