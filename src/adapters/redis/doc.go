// Package redis provides a Redis adapter for GoCell.
//
// This adapter implements caching, distributed rate limiting, session storage,
// and idempotency key management. It wraps the go-redis client and exposes
// interfaces compatible with the rate limiting and cache ports defined in
// kernel/ and runtime/.
//
// Configuration is done via RedisConfig, which can be populated from
// environment variables using ConfigFromEnv().
//
// # Usage
//
//	cfg := redis.ConfigFromEnv()
//	client, err := redis.New(ctx, cfg)
//	if err != nil { ... }
//	defer client.Close()
//
// # Environment Variables
//
// See docs/guides/adapter-config-reference.md for the full variable listing.
// Key variables: REDIS_ADDR, REDIS_PASSWORD, REDIS_DB, REDIS_POOL_SIZE,
// REDIS_DIAL_TIMEOUT, REDIS_READ_TIMEOUT, REDIS_WRITE_TIMEOUT.
//
// # Error Codes
//
// All errors use the ERR_ADAPTER_REDIS_* code family from pkg/errcode.
package redis
