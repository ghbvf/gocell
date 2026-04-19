package redis

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	goredis "github.com/redis/go-redis/v9"
)

// Error codes for the Redis adapter.
const (
	ErrAdapterRedisConnect errcode.Code = "ERR_ADAPTER_REDIS_CONNECT"
	ErrAdapterRedisSet     errcode.Code = "ERR_ADAPTER_REDIS_SET"
	ErrAdapterRedisGet     errcode.Code = "ERR_ADAPTER_REDIS_GET"
	ErrAdapterRedisDelete  errcode.Code = "ERR_ADAPTER_REDIS_DELETE"
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

	// PoolSize is the maximum number of connections go-redis is allowed to
	// maintain. Zero leaves go-redis's default (10 * GOMAXPROCS). Set this
	// explicitly for workloads whose steady-state checkouts would exceed
	// the library default — required for meaningful
	// db.client.connection.max emissions on the pool stats collector.
	PoolSize int
}

// LogValue implements slog.LogValuer so that Config can be safely passed
// to structured loggers without leaking the password.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("mode", string(c.Mode)),
		slog.String("addr", c.Addr),
		slog.Int("db", c.DB),
	)
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
	if c.PoolSize == 0 {
		// Mirror go-redis/v9's own default (10 * GOMAXPROCS) so the
		// derived `db.client.connection.max` metric reflects the real
		// pool capacity. Leaving zero here would emit MaxConns=0, which
		// dashboards interpret as "pool saturated" (used > max).
		c.PoolSize = 10 * runtime.GOMAXPROCS(0)
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

// poolStatsProvider abstracts the PoolStats method available on concrete
// go-redis clients (*Client, *FailoverClient) but not on the cmdable interface.
type poolStatsProvider interface {
	PoolStats() *goredis.PoolStats
}

// PoolStats holds structured connection pool statistics.
//
// ref: go-redis PoolStats / redisprometheus — adopted same field set.
type PoolStats struct {
	Hits       uint32 `json:"hits"`       // times free connection was found in pool
	Misses     uint32 `json:"misses"`     // times free connection was NOT found in pool
	Timeouts   uint32 `json:"timeouts"`   // times a wait timeout occurred
	TotalConns uint32 `json:"totalConns"` // total connections in pool
	IdleConns  uint32 `json:"idleConns"`  // idle connections in pool
	StaleConns uint32 `json:"staleConns"` // stale connections removed from pool
}

// Client wraps a go-redis universal client and provides health checking
// and lifecycle management.
type Client struct {
	rdb           cmdable
	config        Config
	statsProvider poolStatsProvider // nil for test mocks
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
	if cfg.Mode == ModeSentinel && cfg.SentinelMaster == "" {
		return nil, errcode.New(ErrAdapterRedisConnect,
			"redis: Config.SentinelMaster is required for sentinel mode")
	}

	var (
		rdb           cmdable
		statsProvider poolStatsProvider
	)
	switch cfg.Mode {
	case ModeSentinel:
		fc := goredis.NewFailoverClient(&goredis.FailoverOptions{
			MasterName:    cfg.SentinelMaster,
			SentinelAddrs: cfg.SentinelAddrs,
			Password:      cfg.Password,
			DB:            cfg.DB,
			DialTimeout:   cfg.DialTimeout,
			ReadTimeout:   cfg.ReadTimeout,
			WriteTimeout:  cfg.WriteTimeout,
			PoolSize:      cfg.PoolSize,
		})
		rdb = fc
		statsProvider = fc
	default:
		rc := goredis.NewClient(&goredis.Options{
			Addr:         cfg.Addr,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  cfg.DialTimeout,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			PoolSize:     cfg.PoolSize,
		})
		rdb = rc
		statsProvider = rc
	}

	c := &Client{rdb: rdb, config: cfg, statsProvider: statsProvider}

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
//
// Delegates to CloseCtx(context.Background()) for back-compat.
func (c *Client) Close() error {
	return c.CloseCtx(context.Background())
}

// CloseCtx releases the underlying Redis connection, bounded by ctx.
//
// go-redis Client.Close() is synchronous and may block on in-flight commands.
// CloseCtx wraps it in a goroutine so the caller's shutdown budget is honoured;
// if ctx expires, in-flight commands may be abandoned (process-exit semantics).
//
// ref: uber-go/fx app.go StopTimeout — ctx as shared shutdown budget.
func (c *Client) CloseCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		if err := c.rdb.Close(); err != nil {
			done <- errcode.Wrap(ErrAdapterRedisConnect, "redis: close failed", err)
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			return err
		}
		slog.Info("redis: connection closed")
		return nil
	case <-ctx.Done():
		slog.Warn("redis: close budget exceeded",
			slog.Any("error", ctx.Err()))
		return ctx.Err()
	}
}

// Cmdable returns the internal cmdable for use by sibling components
// (DistLock, Cache, IdempotencyClaimer). Not exported.
func (c *Client) cmdable() cmdable {
	return c.rdb
}

// PoolStats returns structured pool statistics suitable for metrics collection.
// Returns zero-value PoolStats for test mocks (no statsProvider).
func (c *Client) PoolStats() PoolStats {
	if c.statsProvider == nil {
		return PoolStats{}
	}
	s := c.statsProvider.PoolStats()
	return PoolStats{
		Hits:       s.Hits,
		Misses:     s.Misses,
		Timeouts:   s.Timeouts,
		TotalConns: s.TotalConns,
		IdleConns:  s.IdleConns,
		StaleConns: s.StaleConns,
	}
}

// Config returns a copy of the client configuration.
// The returned Config is safe to pass to NewClient for round-trip use.
// For logging, Config implements slog.LogValuer which redacts the password.
func (c *Client) Config() Config {
	return c.config
}
