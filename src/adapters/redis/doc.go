// Package redis provides a Redis adapter for the GoCell framework.
//
// It implements cache, session store, and idempotency key interfaces
// defined in kernel/ and runtime/, providing connection pooling, key-value
// operations, TTL-based expiry, and distributed locking.
//
// # Configuration
//
//	Addr:         "localhost:6379"
//	Password:     ""
//	DB:           0
//	PoolSize:     10
//	MinIdleConns: 3
//	DialTimeout:  5s
//
// # Idempotency Keys
//
// The adapter supports SET-IF-ABSENT with TTL for event idempotency keys,
// following the pattern: {prefix}:{group}:{event-id}.
//
// # Close
//
// Always call Close to release the connection pool on shutdown.
package redis
