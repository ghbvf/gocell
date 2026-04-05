package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	goredis "github.com/redis/go-redis/v9"
)

// Compile-time interface check.
var _ idempotency.Checker = (*IdempotencyChecker)(nil)

// IdempotencyChecker implements kernel/idempotency.Checker using Redis
// SET NX + TTL for atomic check-and-mark semantics.
type IdempotencyChecker struct {
	rdb cmdable
}

// NewIdempotencyChecker creates a new IdempotencyChecker using the given Client.
func NewIdempotencyChecker(client *Client) *IdempotencyChecker {
	return &IdempotencyChecker{rdb: client.cmdable()}
}

// newIdempotencyCheckerFromCmdable creates an IdempotencyChecker with a
// pre-built cmdable for testing.
func newIdempotencyCheckerFromCmdable(rdb cmdable) *IdempotencyChecker {
	return &IdempotencyChecker{rdb: rdb}
}

// IsProcessed returns true if the given idempotency key has already been
// marked as processed. It returns false and nil error for keys that do not
// exist (i.e., not yet processed).
func (ic *IdempotencyChecker) IsProcessed(ctx context.Context, key string) (bool, error) {
	val, err := ic.rdb.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return false, nil
		}
		return false, errcode.Wrap(ErrAdapterRedisGet,
			fmt.Sprintf("redis: idempotency check failed (key=%s)", key), err)
	}
	return val == "1", nil
}

// MarkProcessed atomically marks the idempotency key as processed using
// SET NX with the given TTL. If the key already exists (already processed),
// the operation is a no-op and returns nil.
func (ic *IdempotencyChecker) MarkProcessed(ctx context.Context, key string, ttl time.Duration) error {
	_, err := ic.rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: idempotency mark failed (key=%s)", key), err)
	}
	return nil
}
