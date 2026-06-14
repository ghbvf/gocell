package main

import (
	"fmt"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	idemhttp "github.com/ghbvf/gocell/framework/runtime/http/idempotency"
)

type redisHTTPIdempotencyStoreFactory func(*adapterredis.Client, adapterredis.KeyNamespace) (idemhttp.Store, error)

// httpIdempotencyStoreNamespace is the KeyNamespace composition root passes into
// the HTTP idempotency replay store.
//
// This `_runtime` value is the **assembly-wide** HTTP idempotency namespace
// (governance: ADR 202606051000-1449): it is deliberately not pod- or
// cell-specific, so all pods of an assembly that share this Redis form one
// idempotency replay domain (full-assembly scope). Do not derive a per-cell
// namespace for the HTTP store — HTTP idempotency is a cross-cutting concern
// keyed by (tenant, subject, method, path, header), not a per-cell resource.
// The "node-agnostic key" + "stateless store" facts that make this hold are
// frozen by archtests HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01 /
// HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01.
//
// Key collision with the consumer claimer is impossible because the two
// primitives use structurally different key formats — the HTTP store includes a
// request-namespace segment that the consumer claimer omits.
// See adapters/redis/http_idempotency.go (`_runtime:<tenant>:{key}:resp|lease|fp`)
// and adapters/redis/idempotency.go (`_runtime:{eventID}:lease|done`) for the
// format details proving no collision.
const httpIdempotencyStoreNamespace adapterredis.KeyNamespace = "_runtime"

var newRedisHTTPIdempotencyStore redisHTTPIdempotencyStoreFactory = func(
	client *adapterredis.Client, ns adapterredis.KeyNamespace,
) (idemhttp.Store, error) {
	return adapterredis.NewHTTPIdempotencyStore(client, ns)
}

// buildHTTPIdempotencyStore constructs the Redis-backed HTTP idempotency replay
// store when a Redis client is available, or returns (nil, nil) when client is
// nil (memory / single-pod mode has no cross-pod replay need).
//
// The caller in defaultRuntimeOptions checks for a nil return before calling
// bootstrap.WithIdempotencyStore to avoid the typed-nil rejection at phase0.
func buildHTTPIdempotencyStore(client *adapterredis.Client) (idemhttp.Store, error) {
	if client == nil {
		return nil, nil //nolint:nilnil // by design: nil means "not configured"; caller checks before wiring
	}
	store, err := newRedisHTTPIdempotencyStore(client, httpIdempotencyStoreNamespace)
	if err != nil {
		return nil, fmt.Errorf("build Redis HTTP idempotency store: %w", err)
	}
	return store, nil
}
