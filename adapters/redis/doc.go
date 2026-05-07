// Package redis provides a Redis adapter for the GoCell framework.
//
// It wraps github.com/redis/go-redis/v9 and offers:
//   - Client: connection management for standalone, Sentinel, and Cluster modes with Health/Close
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
//   - [runtime/distlock] owns the lifecycle (acquire, renewal scheduling, release timeout, retry budget)
//     via a single shared manager goroutine
//   - [RedisDriver] implements three semantic primitives (SetNX / Renew / Release)
//     using Redis SET NX EX + two Lua scripts (token-matched PEXPIRE / DEL)
//
// Wiring (every constructor below is error-first and takes a KeyNamespace
// — the per-cell or per-role keyspace prefix; see the KeyNamespace godoc
// for naming conventions):
//
//	client, err := redis.NewClient(ctx, redis.Config{Addr: "localhost:6379", Password: "s3cret"})
//	if err != nil { return err }
//
//	driver, err := redis.NewRedisDriver(client, redis.KeyNamespace("accesscore"))
//	if err != nil { return err }
//	locker := distlock.MustNew(driver,
//	    distlock.WithRenewFraction(0.5),
//	    distlock.WithReleaseTimeout(5*time.Second),
//	)
//
//	lockCtx, release, err := locker.Acquire(reqCtx, "key", 30*time.Second)
//	if err != nil { return err }
//	defer func() { _ = release() }()
//	// pass lockCtx to DB / HTTP / outbox calls — they auto-cancel on lock loss
//
// The Cache, IdempotencyClaimer, and NonceStore constructors follow the
// same shape:
//
//	cache, err := redis.NewCache(client, redis.KeyNamespace("accesscore"))
//	claimer, err := redis.NewIdempotencyClaimer(client, redis.KeyNamespace("_runtime"))
//	store, err := redis.NewNonceStore(client, redis.KeyNamespace("servicetoken-nonce"), auth.ServiceTokenNonceTTL)
//
// Composition root (cmd/corebundle) injects the cell ID for per-cell
// resources and the "_runtime" / "servicetoken-nonce" sentinels for
// shared infrastructure with no cell context.
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
//
// # Cluster Mode (B10 PR-V1-REDIS-CLUSTER)
//
// Cluster mode is selected via Config.Mode = ModeCluster and the cluster
// node addresses populated in Config.ClusterAddrs (plain "host:port" or
// "rediss://host:port" URL forms — mixing forms within a single cluster
// definition is rejected). Compared to standalone/sentinel:
//
//   - Config.DB must be 0 (Redis Cluster has no SELECT command).
//   - Config.Addr must be empty (mutual exclusion with ClusterAddrs).
//   - Config.PoolSize is per-node; total cluster connections = nodes × PoolSize.
//   - go-redis ClusterClient handles MOVED/ASK redirection and topology
//     refresh transparently; no business-side retry is required for slot
//     migration scenarios.
//
// IdempotencyClaimer's dual-KEY Lua scripts (claim/commit) require all KEYS
// to map to the same Redis Cluster slot. Keys are wrapped in a hashtag
// `<ns>:{businessKey}:lease` / `<ns>:{businessKey}:done` (the KeyNamespace
// prefix sits OUTSIDE the hashtag) so CRC16 hashes only the business key
// portion; lease and done keys colocate on the same slot under every
// Cluster topology regardless of namespace value. Standalone/Sentinel use
// the same naming for a single source of truth — the hashtag is a no-op
// outside Cluster.
//
// ref: redis/go-redis osscluster.go — ClusterOptions / ParseClusterURL
// ref: Redis cluster-spec hash-tags — {tag} sub-string colocation rule
package redis
