package distlock

import (
	"context"
	"time"
)

// Driver is the storage-backend contract for distributed locks.
// Implementations live in adapters/ (e.g. adapters/redis).
//
// Three semantic primitives encapsulate all backend-specific logic
// (e.g. Lua scripts for Redis), keeping the runtime layer backend-agnostic.
//
// ref: kubernetes/client-go tools/leaderelection/resourcelock/interface.go
//
//	— storage primitives shape adopted; GoCell collapses to 3 methods vs k8s 5
//	  because Get/Update/Create/Delete lifecycle is not needed here.
//
// ref: go-redsync/redsync redsync.go — SetNX/Renew/Release semantics
type Driver interface {
	// SetNX attempts to acquire the lock for key with the given token and TTL.
	// Returns (true, nil) on success, (false, nil) when another holder owns the
	// key (not an error — caller interprets false as "busy"), and (false, err)
	// on I/O failure.
	SetNX(ctx context.Context, key, token string, ttl time.Duration) (acquired bool, err error)

	// Renew extends the TTL of an existing lock only if token still matches.
	// Returns (true, nil) on success, (false, nil) when the token no longer
	// matches (ownership lost — not an I/O error), and (false, err) on I/O failure.
	Renew(ctx context.Context, key, token string, ttl time.Duration) (held bool, err error)

	// Release deletes the lock key only if token still matches.
	// Returns nil on success or when the key is already gone (idempotent).
	// Returns a non-nil error only on I/O failure.
	Release(ctx context.Context, key, token string) error
}
