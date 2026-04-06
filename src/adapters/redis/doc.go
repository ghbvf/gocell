// Package redis provides a Redis adapter for the GoCell framework.
//
// It wraps github.com/redis/go-redis/v9 and offers:
//   - Client: connection management for standalone and Sentinel modes with Health/Close
//   - DistLock: distributed locking with TTL-based acquire/release and automatic renewal
//   - IdempotencyChecker: kernel/idempotency.Checker implementation using SET NX + TTL
//   - Cache: typed Get/Set/Delete with TTL and JSON generics helpers
//
// Configuration follows the Options pattern inspired by go-micro store/redis.
// Error codes use the ERR_ADAPTER_REDIS_* prefix via pkg/errcode.
//
// # Distributed Locking Safety
//
// DistLock provides distributed mutual exclusion on a best-effort basis.
// It is suitable for efficiency (avoiding duplicate work) but does NOT
// guarantee correctness in the face of lock expiry during GC pauses,
// network delays, or clock skew — consistent with redsync, rueidis, and
// all major Redis lock libraries.
//
// For correctness-critical paths, use [Lock.FenceToken] to obtain a
// monotonically increasing token and enforce it at the downstream store
// (e.g., UPDATE ... WHERE fence_token < $1). The lock reduces contention;
// the conditional write guarantees safety.
//
// # Lock Lifecycle
//
// The Acquire context only governs the acquisition attempt (SetNX). The
// renewal goroutine runs independently until [Lock.Release] is called.
// Always release with a bounded cleanup context, not the request context:
//
//	lock, err := dl.Acquire(requestCtx, key, ttl)
//	if err != nil { return err }
//	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	defer lock.Release(cleanupCtx)
//
// ref: Martin Kleppmann "How to do distributed locking" (2016)
// ref: go-redsync/redsync — no fencing tokens, manual Extend
// ref: redis/rueidis/rueidislock — context-as-lock, no fencing tokens
package redis
