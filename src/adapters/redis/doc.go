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
package redis
