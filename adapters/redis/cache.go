package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const internalKeyFmt = "key=%s"

// Cache provides a typed key-value cache backed by Redis. All keys are
// prefixed with the constructor-injected KeyNamespace, giving each owner
// (cell or shared role) an isolated keyspace.
type Cache struct {
	rdb cmdable
	ns  KeyNamespace
}

// NewCache creates a new Cache using the given Client and KeyNamespace.
// ns is validated up front; an invalid namespace produces an error.
func NewCache(client *Client, ns KeyNamespace) (*Cache, error) {
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	return &Cache{rdb: client.cmdable(), ns: ns}, nil
}

// newCacheFromCmdable creates a Cache with a pre-built cmdable for testing.
func newCacheFromCmdable(rdb cmdable, ns KeyNamespace) (*Cache, error) {
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	return &Cache{rdb: rdb, ns: ns}, nil
}

// Get retrieves the raw string value for the given key.
// Returns ("", nil) when the key does not exist.
func (c *Cache) Get(ctx context.Context, key string) (string, error) {
	prefixed := c.ns.apply(key)
	val, err := c.rdb.Get(ctx, prefixed).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return "", nil
		}
		return "", errcode.Wrap(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: cache get failed", err,
			errcode.WithInternal(fmt.Sprintf(internalKeyFmt, prefixed)))
	}
	return val, nil
}

// Set stores a string value with the given TTL.
// A zero TTL means the key does not expire.
func (c *Cache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	prefixed := c.ns.apply(key)
	if err := c.rdb.Set(ctx, prefixed, value, ttl).Err(); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterRedisSet,
			"redis: cache set failed", err,
			errcode.WithInternal(fmt.Sprintf(internalKeyFmt, prefixed)))
	}
	return nil
}

// Delete removes the given key from the cache.
// Deleting a non-existent key is a no-op and returns nil.
func (c *Cache) Delete(ctx context.Context, key string) error {
	prefixed := c.ns.apply(key)
	if err := c.rdb.Del(ctx, prefixed).Err(); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterRedisDelete,
			"redis: cache delete failed", err,
			errcode.WithInternal(fmt.Sprintf(internalKeyFmt, prefixed)))
	}
	return nil
}

// GetJSON retrieves the value for the given key and JSON-decodes it into T.
// Returns the zero value and nil error when the key does not exist.
func GetJSON[T any](ctx context.Context, c *Cache, key string) (T, error) {
	var zero T
	prefixed := c.ns.apply(key)
	raw, err := c.rdb.Get(ctx, prefixed).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return zero, nil
		}
		return zero, errcode.Wrap(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: cache get json failed", err,
			errcode.WithInternal(fmt.Sprintf(internalKeyFmt, prefixed)))
	}
	var result T
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return zero, errcode.Wrap(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: cache json unmarshal failed", err,
			errcode.WithInternal(fmt.Sprintf(internalKeyFmt, prefixed)))
	}
	return result, nil
}

// SetJSON JSON-encodes the value and stores it with the given TTL.
// A zero TTL means the key does not expire.
func SetJSON[T any](ctx context.Context, c *Cache, key string, value T, ttl time.Duration) error {
	prefixed := c.ns.apply(key)
	data, err := json.Marshal(value)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterRedisSet,
			"redis: cache json marshal failed", err,
			errcode.WithInternal(fmt.Sprintf(internalKeyFmt, prefixed)))
	}
	if err := c.rdb.Set(ctx, prefixed, string(data), ttl).Err(); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterRedisSet,
			"redis: cache set json failed", err,
			errcode.WithInternal(fmt.Sprintf(internalKeyFmt, prefixed)))
	}
	return nil
}
