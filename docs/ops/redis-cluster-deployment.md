# Redis Cluster Deployment Guide

GoCell's Redis adapter supports three modes: `standalone`, `sentinel`, and
`cluster`. This document covers cluster mode (B10 — PR-V1-REDIS-CLUSTER),
which is required for AWS ElastiCache Cluster, Azure Cache Cluster, and any
self-hosted Redis Cluster deployment.

## When to use cluster mode

Choose cluster mode when:

- Total dataset size exceeds a single Redis node's RAM and you need horizontal
  sharding.
- You operate on a managed offering (AWS ElastiCache Cluster, Azure Cache
  Cluster, GCP Memorystore Cluster) that exposes a cluster-mode endpoint.
- You need automatic failover with sharded keyspace; Sentinel only handles
  primary/replica failover within a single shard.

If your dataset fits a single node and you only need HA failover, prefer
**sentinel** mode. If you have one node total, use **standalone**. Cluster
adds operational complexity (slot rebalancing, gossip topology, multi-key
constraints) — do not adopt it without justification.

## Configuration

Two environment variables must be set on every pod:

| Variable | Example | Notes |
|---|---|---|
| `GOCELL_REDIS_CLUSTER_ADDRS` | `rediss://node-a.example.internal:7000,rediss://node-b.example.internal:7000,rediss://node-c.example.internal:7000` | Comma-separated cluster node seed addresses. Production deployments must use `rediss://` URL form (TLS); the GoCell `pkg/secutil.ValidateTLSEndpoint` fail-closed rule rejects bare `host:port` for any non-loopback address. The go-redis client discovers the rest of the topology at startup. |
| `GOCELL_REDIS_PASSWORD` | _(secret manager)_ | Leave unset if your cluster has no AUTH. |

`GOCELL_REDIS_ADDR` and `GOCELL_REDIS_CLUSTER_ADDRS` are mutually exclusive; setting
both fails fast at startup with `ERR_VALIDATION_FAILED`.

`GOCELL_REDIS_DB` must be `0` (or unset). Redis Cluster has no `SELECT`
command — non-zero values fail fast.

### TLS (rediss URL form)

For TLS-secured clusters use the URL form per address:

```
GOCELL_REDIS_CLUSTER_ADDRS=rediss://node-a.example.internal:7000,rediss://node-b.example.internal:7000
```

GoCell's TLS validator (`pkg/secutil.ValidateTLSEndpoint`) rejects bare remote
`host:port` addresses — only loopback (`127.0.0.1`, `::1`, `localhost`) is
accepted without `rediss://`. This is fail-closed by design.

The shared `TLSConfig` is derived from the first `rediss://` URL with
`ServerName=""` so `crypto/tls` infers SNI from each per-node dial target.
Per-URL credentials are merged across the seed list; conflicting values fail
fast.

Mixing URL and bare `host:port` forms within a single value is rejected to
avoid silent TLS downgrade.

## Connection pool sizing

`Config.PoolSize` is **per-node**. Total cluster connections at saturation
equals `nodes × PoolSize`.

When `Config.PoolSize` is zero, GoCell applies a per-mode default:

- standalone / sentinel: `10 × GOMAXPROCS`
- cluster: `5 × GOMAXPROCS` (per node)

The cluster default is halved relative to standalone so aggregate
saturation-time connection counts stay comparable across modes (a 6-node
cluster with default `5 × GOMAXPROCS` per node ≈ 30 × GOMAXPROCS aggregate,
vs. a single standalone node at 10 × GOMAXPROCS).

OTel's `db.client.connection.max` reports the per-pool ceiling (matching the
semantic convention); the cluster-wide total is derived at the dashboard layer
as `nodes × db.client.connection.max`.

## Multi-key operations and hashtags

Redis Cluster requires every multi-key operation (Lua `EVAL` with multiple
KEYS, `MGET`, `MSET`, etc.) to address keys that hash to the same slot.
Cross-slot accesses return `CROSSSLOT Keys in request don't hash to the same
slot`.

GoCell's `IdempotencyClaimer` uses two KEYS per Lua script (`{key}:lease` and
`{key}:done`). The keys are wrapped in a Redis Cluster hashtag so the CRC16
slot is computed only over the business key portion — both keys colocate on
the same slot for any topology.

If you add new multi-key operations to this adapter, audit them for
cluster-safety. The archtest gate `IDEMPOTENCY-LUA-HASHTAG-01` (in
`tools/archtest/`) statically checks the existing claimer key construction;
extend it (or add a sibling gate) when introducing new multi-key call sites.

## Operational notes

- **MOVED / ASK redirection** is handled transparently by go-redis. Business
  code never sees these errors. Routing tables refresh automatically when the
  cluster topology changes.
- **Resharding (slot migration)** can produce `TRYAGAIN` for multi-key
  operations; go-redis retries internally. If the retry budget is exceeded,
  GoCell's `IdempotencyClaimer.Claim` wraps the error with
  `ERR_ADAPTER_REDIS_SET`, which `ConsumerBase` requeues. No business-side
  retry logic is required.
- **Read-only replica routing** is not enabled. We use the cluster's primary
  shards for all operations to keep idempotency, lease, and nonce semantics
  strongly consistent. If you need eventual-consistency reads later, set
  `RouteByLatency` or `RouteRandomly` via a fork-and-extend (currently not
  exposed in `Config`).

## Local cluster for testing

For running `adapters/redis` integration tests against a real cluster:

```sh
# Linux (host networking — gossip addresses on 127.0.0.1 are reachable):
docker run --rm -d --network host --name gocell-test-redis-cluster \
    grokzen/redis-cluster:7.0.10

GOCELL_TEST_REDIS_CLUSTER_ADDRS=127.0.0.1:7000,127.0.0.1:7001,127.0.0.1:7002 \
    go test -tags=integration_cluster ./adapters/redis/...
```

macOS / Windows Docker Desktop do not honor `--network host` for Linux
containers; the cluster-cluster integration test will not pass there without
manual port forwarding. CI runs Linux so the integration tag works in CI when
explicitly enabled.

`GOCELL_TEST_REDIS_CLUSTER_ADDRS` is the test-side discovery env, distinct
from the production-side `GOCELL_REDIS_CLUSTER_ADDRS`. Setting one does not
imply the other.

## Migration from standalone / sentinel

Replacing `GOCELL_REDIS_ADDR` (or sentinel addrs) with
`GOCELL_REDIS_CLUSTER_ADDRS` requires a coordinated cutover:

1. Stop traffic to the old Redis (or accept that in-flight idempotency keys
   continue to be served by the old store until they expire — `done` keys
   live for 24h by default, leases for 5 minutes).
2. Update `GOCELL_REDIS_CLUSTER_ADDRS` and unset `GOCELL_REDIS_ADDR` /
   `GOCELL_REDIS_DB` on every pod.
3. Roll pods. New pods write keys with the cluster-safe `{key}:lease` /
   `{key}:done` naming on the new cluster.

Existing key data on the old standalone/sentinel deployment is **not**
migrated. Idempotency keys age out of relevance within their TTL window;
service-token nonces age out within `auth.ServiceTokenNonceTTL` (~5 minutes).
The cutover window therefore tolerates re-processing of in-flight messages
(consumer-side idempotency stays intact via the new claimer keys) and a
brief replay-detection gap that aligns with normal pod-restart behavior.
