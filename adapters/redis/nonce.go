package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

const serviceTokenNoncePrefix = "servicetoken:nonce:"

// NonceStore implements auth.NonceStore using Redis SET NX EX semantics.
// It is safe for multi-pod service-token replay protection because every
// first-use claim is coordinated by Redis.
type NonceStore struct {
	rdb cmdable
	ttl time.Duration
}

var _ auth.NonceStore = (*NonceStore)(nil)

// NewNonceStore creates a Redis-backed service-token nonce store.
func NewNonceStore(client *Client, ttl time.Duration) (*NonceStore, error) {
	if client == nil {
		return nil, errcode.New(ErrAdapterRedisConnect, "redis nonce store: client is nil")
	}
	return newNonceStoreFromCmdable(client.cmdable(), ttl)
}

func newNonceStoreFromCmdable(rdb cmdable, ttl time.Duration) (*NonceStore, error) {
	if rdb == nil {
		return nil, errcode.New(ErrAdapterRedisConnect, "redis nonce store: cmdable is nil")
	}
	if ttl <= 0 {
		return nil, errcode.New(ErrAdapterRedisSet,
			fmt.Sprintf("redis nonce store: ttl must be positive, got %v", ttl))
	}
	if ttl < auth.ServiceTokenNonceTTL {
		return nil, errcode.New(ErrAdapterRedisSet,
			fmt.Sprintf("redis nonce store: ttl %v is shorter than ServiceTokenNonceTTL %v; a shorter TTL reintroduces the replay window",
				ttl, auth.ServiceTokenNonceTTL))
	}
	return &NonceStore{rdb: rdb, ttl: ttl}, nil
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
	key := serviceTokenNoncePrefix + nonce
	ok, err := s.rdb.SetNX(ctx, key, "1", s.ttl).Result()
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis nonce store: SET NX failed (key=%s)", key), err)
	}
	if !ok {
		return auth.ErrNonceReused
	}
	return nil
}
