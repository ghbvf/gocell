package distlock

import (
	"context"
	"time"
)

// Locker acquires named distributed locks. Backend implementations live in
// adapters/ (currently only adapters/redis). Provider-neutral so runtime/
// and cells/ can depend on the contract without importing a specific adapter.
//
// ref: PR#177 runtime/outbox.Store — identical layering rationale
// ref: github.com/go-redsync/redsync mutex.go — Lock/Unlock/Extend shape rejected
// because GoCell auto-renews; Extend() on the contract would be backend-specific
type Locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, error)
}

// Lock is one acquired grant. Release MUST be called with a fresh context
// (NOT the Acquire context) so cleanup survives request-scope cancellation.
//
// Lost returns a channel that is closed when the lock is known to have been
// lost (background renewal failed or another holder took over). Callers that
// do non-reentrant work should select on Lost() to abort before the grant
// is gone. Current redis impl closes lost on renewal failure; impls without
// background renewal MAY close lost on Release and no other time.
//
// ref: github.com/hashicorp/consul/api lock.go — lostCh pattern
// ref: github.com/temporalio/sdk-go AggregatedWorker.stopC — signal-by-close idiom
type Lock interface {
	Release(ctx context.Context) error
	Key() string
	Lost() <-chan struct{}
}
