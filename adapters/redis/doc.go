// Package redis provides a Redis adapter for the GoCell framework.
//
// It wraps github.com/redis/go-redis/v9 and offers:
//   - Client: connection management for standalone and Sentinel modes with Health/Close
//   - RedisDriver: implements [runtime/distlock.Driver] (SetNX / Renew / Release Lua primitives)
//     so [runtime/distlock.New] can construct a Locker backed by Redis
//   - IdempotencyClaimer: kernel/idempotency.Claimer implementation using two-phase Claim/Commit/Release with Lua scripts
//   - Cache: typed Get/Set/Delete with TTL and JSON generics helpers
//
// Configuration follows the Options pattern inspired by go-micro store/redis.
// Error codes use the ERR_ADAPTER_REDIS_* prefix via pkg/errcode for adapter-
// specific failures (connect / ping). Distributed-lock errors live in
// runtime/distlock (ERR_DISTLOCK_*) because the Locker contract is defined
// there — see runtime/distlock/errors.go.
//
// # Distributed Locking
//
// Distributed locking is a runtime/adapter split:
//   - [runtime/distlock] owns the lifecycle (acquire, renewal scheduling, release timeout, retry budget) via a single shared manager goroutine
//   - [RedisDriver] implements three semantic primitives (SetNX / Renew / Release)
//     using Redis SET NX EX + two Lua scripts (token-matched PEXPIRE / DEL)
//
// Wiring:
//
//	rdb := goredis.NewClient(&goredis.Options{Addr: "localhost:6379"})
//	driver := redis.NewRedisDriver(rdb)
//	locker := distlock.New(driver,
//	    distlock.WithRenewFraction(0.5),
//	    distlock.WithReleaseTimeout(5*time.Second),
//	)
//
//	lockCtx, release, err := locker.Acquire(reqCtx, "key", 30*time.Second)
//	if err != nil { return err }
//	defer func() { _ = release() }()
//	// pass lockCtx to DB / HTTP / outbox calls — they auto-cancel on lock loss
//
// # Distributed Locking Safety
//
// Best-effort mutual exclusion. Suitable for efficiency (avoiding duplicate
// work) but does NOT guarantee correctness in the face of lock expiry during
// GC pauses, network delays, or clock skew — consistent with redsync, rueidis,
// and all major Redis lock libraries.
//
// For correctness-critical paths, use application-level conditional writes
// (e.g., Postgres optimistic locking with row versions). The lock reduces
// contention; the conditional write guarantees safety.
//
// ref: Martin Kleppmann "How to do distributed locking" (2016)
// ref: go-redsync/redsync redis/redis.go — Driver primitive split
// ref: kubernetes/client-go tools/leaderelection/resourcelock — runtime/adapter layering
package redis
