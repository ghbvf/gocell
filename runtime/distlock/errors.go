package distlock

import "github.com/ghbvf/gocell/pkg/errcode"

// Error codes for distributed lock operations. Values are stable string IDs
// consumed by client-side error taxonomies and must not change without a
// coordinated rollout. Matches the fail-fast naming convention in pkg/errcode
// (verb, not past tense — corrects the original ERR_ADAPTER_REDIS_LOCK_ACQUIRED
// typo from adapters/redis/client.go).
const (
	ErrLockAcquire errcode.Code = "ERR_DISTLOCK_ACQUIRE" // acquire I/O failure
	ErrLockRelease errcode.Code = "ERR_DISTLOCK_RELEASE" // release I/O failure
	ErrLockTimeout errcode.Code = "ERR_DISTLOCK_TIMEOUT" // another holder owns the key
	ErrLockLost    errcode.Code = "ERR_DISTLOCK_LOST"    // renewal failed / ownership lost
)
