package redis

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// NonceStore implements auth.NonceStore using Redis SET NX EX semantics.
// It is safe for multi-pod service-token replay protection because every
// first-use claim is coordinated by Redis.
//
// Keys land under the constructor-injected KeyNamespace, e.g.
// `servicetoken-nonce:<nonce>`. The namespace replaces the previous
// hard-coded `servicetoken:nonce:` prefix; layering namespace + role
// constant added no information beyond what KeyNamespace itself encodes.
type NonceStore struct {
	rdb cmdable
	ns  KeyNamespace
	ttl time.Duration
}

var _ auth.NonceStore = (*NonceStore)(nil)

// NewNonceStore creates a Redis-backed service-token nonce store.
//
// The body validates `ns` before the `client == nil` check so the archtest
// REDIS-KEY-NAMESPACE-01 gate (which requires `ns.Validate()` near the top
// of every public Redis constructor) sees the call within its statement
// budget. The internal helper newNonceStoreFromCmdable re-validates as
// defense-in-depth: integration tests bypass the public constructor by
// calling the helper directly, so the helper has to police its own input.
func NewNonceStore(client *Client, ns KeyNamespace, ttl time.Duration) (*NonceStore, error) {
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect, "redis nonce store: client is nil")
	}
	return newNonceStoreFromCmdable(client.cmdable(), ns, ttl)
}

// newNonceStoreFromCmdable is the cmdable-level constructor. It is also
// called directly from tests, so it re-validates `ns` to maintain the
// invariant that no NonceStore can be assembled with a malformed
// namespace — see NewNonceStore's doc comment for context.
func newNonceStoreFromCmdable(rdb cmdable, ns KeyNamespace, ttl time.Duration) (*NonceStore, error) {
	if rdb == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect, "redis nonce store: cmdable is nil")
	}
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterRedisSet,
			"redis nonce store: ttl must be positive",
			errcode.WithDetails(slog.String("ttl", ttl.String())))
	}
	if ttl < auth.ServiceTokenNonceTTL {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterRedisSet,
			"redis nonce store: ttl shorter than ServiceTokenNonceTTL; replay window reintroduced",
			errcode.WithDetails(slog.String("ttl", ttl.String()), slog.String("min", auth.ServiceTokenNonceTTL.String())))
	}
	return &NonceStore{rdb: rdb, ns: ns, ttl: ttl}, nil
}

// Kind reports that this store is distributed and safe for multi-pod replay
// protection when Redis itself is available.
func (*NonceStore) Kind() auth.NonceStoreKind {
	return auth.NonceStoreKindDistributed
}

// CheckAndMark records nonce if it has not been seen within the TTL window.
// Replays return auth.ErrNonceReused so service-token middleware can map the
// condition to ERR_AUTH_REPLAY_DETECTED.
func (s *NonceStore) CheckAndMark(ctx context.Context, nonce string) error {
	key := s.ns.apply(nonce)
	ok, err := s.rdb.SetNX(ctx, key, "1", s.ttl).Result()
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterRedisSet,
			"redis nonce store: SET NX failed", err,
			errcode.WithInternal(fmt.Sprintf("key=%s", key)))
	}
	if !ok {
		return auth.ErrNonceReused
	}
	return nil
}
