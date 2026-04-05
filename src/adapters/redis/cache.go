package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	goredis "github.com/redis/go-redis/v9"
)

// Cache provides a typed key-value cache backed by Redis.
type Cache struct {
	rdb cmdable
}

// NewCache creates a new Cache using the given Client.
func NewCache(client *Client) *Cache {
	return &Cache{rdb: client.cmdable()}
}

// newCacheFromCmdable creates a Cache with a pre-built cmdable for testing.
func newCacheFromCmdable(rdb cmdable) *Cache {
	return &Cache{rdb: rdb}
}

// Get retrieves the raw string value for the given key.
// Returns ("", nil) when the key does not exist.
func (c *Cache) Get(ctx context.Context, key string) (string, error) {
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return "", nil
		}
		return "", errcode.Wrap(ErrAdapterRedisGet,
			fmt.Sprintf("redis: cache get failed (key=%s)", key), err)
	}
	return val, nil
}

// Set stores a string value with the given TTL.
// A zero TTL means the key does not expire.
func (c *Cache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	if err := c.rdb.Set(ctx, key, value, ttl).Err(); err != nil {
		return errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: cache set failed (key=%s)", key), err)
	}
	return nil
}

// Delete removes the given key from the cache.
// Deleting a non-existent key is a no-op and returns nil.
func (c *Cache) Delete(ctx context.Context, key string) error {
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		return errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: cache delete failed (key=%s)", key), err)
	}
	return nil
}

// GetJSON retrieves the value for the given key and JSON-decodes it into T.
// Returns the zero value and nil error when the key does not exist.
func GetJSON[T any](ctx context.Context, c *Cache, key string) (T, error) {
	var zero T
	raw, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return zero, nil
		}
		return zero, errcode.Wrap(ErrAdapterRedisGet,
			fmt.Sprintf("redis: cache get json failed (key=%s)", key), err)
	}
	var result T
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return zero, errcode.Wrap(ErrAdapterRedisGet,
			fmt.Sprintf("redis: cache json unmarshal failed (key=%s)", key), err)
	}
	return result, nil
}

// SetJSON JSON-encodes the value and stores it with the given TTL.
// A zero TTL means the key does not expire.
func SetJSON[T any](ctx context.Context, c *Cache, key string, value T, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: cache json marshal failed (key=%s)", key), err)
	}
	if err := c.rdb.Set(ctx, key, string(data), ttl).Err(); err != nil {
		return errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: cache set json failed (key=%s)", key), err)
	}
	return nil
}
