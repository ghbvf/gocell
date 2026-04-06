package redis

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	goredis "github.com/redis/go-redis/v9"
)

// Error codes for the Redis adapter.
const (
	ErrAdapterRedisConnect     errcode.Code = "ERR_ADAPTER_REDIS_CONNECT"
	ErrAdapterRedisLockAcquire errcode.Code = "ERR_ADAPTER_REDIS_LOCK_ACQUIRE"
	ErrAdapterRedisLockRelease errcode.Code = "ERR_ADAPTER_REDIS_LOCK_RELEASE"
	ErrAdapterRedisLockTimeout errcode.Code = "ERR_ADAPTER_REDIS_LOCK_TIMEOUT"
	ErrAdapterRedisSet         errcode.Code = "ERR_ADAPTER_REDIS_SET"
	ErrAdapterRedisGet         errcode.Code = "ERR_ADAPTER_REDIS_GET"
	ErrAdapterRedisDelete      errcode.Code = "ERR_ADAPTER_REDIS_DELETE"
)

// Mode represents the Redis deployment topology.
type Mode string

const (
	// ModeStandalone connects to a single Redis instance.
	ModeStandalone Mode = "standalone"
	// ModeSentinel connects via Redis Sentinel for high availability.
	ModeSentinel Mode = "sentinel"
)

// Config holds connection and behavioral settings for the Redis adapter.
type Config struct {
	// Addr is the address of the standalone Redis instance (e.g. "localhost:6379").
	Addr string

	// SentinelAddrs is the list of Sentinel addresses for Sentinel mode.
	SentinelAddrs []string

	// SentinelMaster is the name of the master instance for Sentinel mode.
	SentinelMaster string

	// Mode selects standalone or sentinel. Defaults to ModeStandalone.
	Mode Mode

	// Password is the auth password, if any.
	Password string

	// DB is the database number. Defaults to 0.
	DB int

	// DialTimeout is the connection dial timeout. Defaults to 5s.
	DialTimeout time.Duration

	// ReadTimeout is the read timeout. Defaults to 3s.
	ReadTimeout time.Duration

	// WriteTimeout is the write timeout. Defaults to ReadTimeout.
	WriteTimeout time.Duration

	// DistLockTTL is the default TTL for distributed locks. Defaults to 30s.
	DistLockTTL time.Duration
}

// defaults applies default values to zero-valued fields.
func (c *Config) defaults() {
	if c.Mode == "" {
		c.Mode = ModeStandalone
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = 5 * time.Second
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 3 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = c.ReadTimeout
	}
	if c.DistLockTTL == 0 {
		c.DistLockTTL = 30 * time.Second
	}
}

// cmdable is an internal interface matching the subset of redis.Cmdable
// used by this package. It enables unit testing with mock implementations.
type cmdable interface {
	Ping(ctx context.Context) *goredis.StatusCmd
	Close() error
	Set(ctx context.Context, key string, value any, expiration time.Duration) *goredis.StatusCmd
	Get(ctx context.Context, key string) *goredis.StringCmd
	Del(ctx context.Context, keys ...string) *goredis.IntCmd
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *goredis.BoolCmd
	Eval(ctx context.Context, script string, keys []string, args ...any) *goredis.Cmd
}

// Client wraps a go-redis universal client and provides health checking
// and lifecycle management.
type Client struct {
	rdb    cmdable
	config Config
}

// NewClient creates a new Redis Client with the given configuration.
// It pings the server to verify connectivity on creation.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	cfg.defaults()

	if cfg.Mode == ModeStandalone && cfg.Addr == "" {
		return nil, errcode.New(ErrAdapterRedisConnect,
			"redis: Config.Addr is required for standalone mode")
	}
	if cfg.Mode == ModeSentinel && len(cfg.SentinelAddrs) == 0 {
		return nil, errcode.New(ErrAdapterRedisConnect,
			"redis: Config.SentinelAddrs is required for sentinel mode")
	}

	var rdb cmdable
	switch cfg.Mode {
	case ModeSentinel:
		rdb = goredis.NewFailoverClient(&goredis.FailoverOptions{
			MasterName:    cfg.SentinelMaster,
			SentinelAddrs: cfg.SentinelAddrs,
			Password:      cfg.Password,
			DB:            cfg.DB,
			DialTimeout:   cfg.DialTimeout,
			ReadTimeout:   cfg.ReadTimeout,
			WriteTimeout:  cfg.WriteTimeout,
		})
	default:
		rdb = goredis.NewClient(&goredis.Options{
			Addr:         cfg.Addr,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  cfg.DialTimeout,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
		})
	}

	c := &Client{rdb: rdb, config: cfg}

	if err := c.Health(ctx); err != nil {
		// Close to avoid resource leak on failed initial connection.
		if closeErr := rdb.Close(); closeErr != nil {
			slog.Warn("redis: failed to close client after health check failure",
				"error", closeErr)
		}
		return nil, err
	}

	slog.Info("redis: connected",
		"mode", string(cfg.Mode),
		"addr", cfg.Addr,
		"db", cfg.DB)

	return c, nil
}

// newClientFromCmdable creates a Client with a pre-built cmdable.
// This is used for testing.
func newClientFromCmdable(rdb cmdable, cfg Config) *Client {
	cfg.defaults()
	return &Client{rdb: rdb, config: cfg}
}

// Health pings the Redis server and returns an error if it is unreachable.
func (c *Client) Health(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return errcode.Wrap(ErrAdapterRedisConnect,
			fmt.Sprintf("redis: health check failed (addr=%s)", c.config.Addr), err)
	}
	return nil
}

// Close releases the underlying Redis connection.
func (c *Client) Close() error {
	if err := c.rdb.Close(); err != nil {
		return errcode.Wrap(ErrAdapterRedisConnect, "redis: close failed", err)
	}
	slog.Info("redis: connection closed")
	return nil
}

// Cmdable returns the internal cmdable for use by sibling components
// (DistLock, Cache, IdempotencyChecker). Not exported.
func (c *Client) cmdable() cmdable {
	return c.rdb
}

// Config returns a copy of the client configuration with the password redacted.
func (c *Client) Config() Config {
	cfg := c.config
	if cfg.Password != "" {
		cfg.Password = "***"
	}
	return cfg
}
