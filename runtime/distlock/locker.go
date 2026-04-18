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
// The Lock follows a collapsed end-signal model (Consul api/lock.go +
// Kubernetes leaderelection-style): Lost() closes when the grant ends for
// ANY reason — background renewal failure, ownership taken by another
// holder, the lock TTL expired naturally, OR Release completing normally.
// Callers that do non-reentrant work select on Lost() to know "the grant
// is no longer yours" without distinguishing how it ended. When distinction
// matters, inspect Release's return value.
//
// ref: github.com/hashicorp/consul/api lock.go — collapsed lostCh pattern
// ref: github.com/temporalio/sdk-go AggregatedWorker.stopC — signal-by-close idiom
// ref: github.com/go-redsync/redsync mutex.go — rejected (separate Extend()
//
//	not needed: GoCell auto-renews; splitting release outcomes instead lives
//	in the Release error return, not in a second channel)
type Lock interface {
	Release(ctx context.Context) error
	Key() string
	Lost() <-chan struct{}
}
