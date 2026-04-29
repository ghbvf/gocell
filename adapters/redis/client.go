package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/secutil"
	goredis "github.com/redis/go-redis/v9"
)

// Compile-time assertion: Client implements lifecycle.ContextCloser.
var _ lifecycle.ContextCloser = (*Client)(nil)

// Error codes for the Redis adapter.
const (
	ErrAdapterRedisConnect errcode.Code = "ERR_ADAPTER_REDIS_CONNECT"
	ErrAdapterRedisSet     errcode.Code = "ERR_ADAPTER_REDIS_SET"
	ErrAdapterRedisGet     errcode.Code = "ERR_ADAPTER_REDIS_GET"
	ErrAdapterRedisDelete  errcode.Code = "ERR_ADAPTER_REDIS_DELETE"
)

const (
	// defaultRedisDialTimeout is the default TCP connection timeout.
	defaultRedisDialTimeout = 5 * time.Second
	// defaultRedisReadTimeout is the default socket read timeout.
	defaultRedisReadTimeout = 3 * time.Second
	// defaultRedisDistLockTTL is the default distributed lock TTL.
	defaultRedisDistLockTTL = 30 * time.Second
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
		c.DialTimeout = defaultRedisDialTimeout
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = defaultRedisReadTimeout
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = c.ReadTimeout
	}
	if c.DistLockTTL == 0 {
		c.DistLockTTL = defaultRedisDistLockTTL
	}
	if c.PoolSize == 0 {
		// Mirror go-redis/v9's own default (10 * GOMAXPROCS) so the
		// derived `db.client.connection.max` metric reflects the real
		// pool capacity. Leaving zero here would emit MaxConns=0, which
		// dashboards interpret as "pool saturated" (used > max).
		c.PoolSize = 10 * runtime.GOMAXPROCS(0)
	}
}

// validateEndpointTLS enforces SEC-FAIL-CLOSED: all addresses must use a
// TLS-secured scheme or be loopback (127.0.0.1, ::1, localhost) for dev/CI.
func (c *Config) validateEndpointTLS() error {
	if c.Mode == ModeStandalone {
		return secutil.ValidateTLSEndpoint(c.Addr)
	}
	for _, addr := range c.SentinelAddrs {
		if err := secutil.ValidateTLSEndpoint(addr); err != nil {
			return err
		}
	}
	return nil
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

	// SEC-FAIL-CLOSED: validate TLS before any network dial.
	if err := cfg.validateEndpointTLS(); err != nil {
		return nil, err
	}

	var (
		rdb           cmdable
		statsProvider poolStatsProvider
	)
	switch cfg.Mode {
	case ModeSentinel:
		opts, err := buildFailoverOptions(cfg)
		if err != nil {
			return nil, err
		}
		fc := goredis.NewFailoverClient(opts)
		rdb = fc
		statsProvider = fc
	default:
		// SEC-FAIL-CLOSED: build Options via ParseURL when Addr is a URL form
		// so that rediss://host:port carries TLSConfig into the dial. Without
		// this, go-redis silently downgrades to plain TCP and ValidateTLSEndpoint
		// becomes a paper guarantee.
		opts, err := buildStandaloneOptions(cfg)
		if err != nil {
			return nil, err
		}
		rc := goredis.NewClient(opts)
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

// buildStandaloneOptions converts cfg into goredis.Options. When cfg.Addr is a
// URL form (`rediss://...` or `redis://...`), the URL is parsed via
// goredis.ParseURL so that TLSConfig populated by `rediss` reaches go-redis;
// without this step the new Standalone client would dial plain TCP regardless
// of the scheme, and SEC-FAIL-CLOSED ValidateTLSEndpoint would be a paper-only
// guarantee. For plain host:port input the function passes through unchanged.
//
// Explicit Config fields (Password, DB) take precedence over URL-encoded values
// when both are set, so Config remains the single source of truth in tests.
func buildStandaloneOptions(cfg Config) (*goredis.Options, error) {
	base := &goredis.Options{
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolSize:     cfg.PoolSize,
	}
	if !strings.Contains(cfg.Addr, "://") {
		base.Addr = cfg.Addr
		return base, nil
	}
	parsed, err := goredis.ParseURL(cfg.Addr)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterRedisConnect,
			fmt.Sprintf("redis: invalid Addr URL %q", cfg.Addr), err)
	}
	base.Addr = parsed.Addr
	base.TLSConfig = parsed.TLSConfig
	if base.Password == "" && parsed.Password != "" {
		base.Password = parsed.Password
	}
	if base.DB == 0 && parsed.DB != 0 {
		base.DB = parsed.DB
	}
	return base, nil
}

// buildFailoverOptions converts cfg into go-redis FailoverOptions. Sentinel
// mode accepts either plain host:port entries for loopback dev/test use, or URL
// entries (`rediss://host:port`) for TLS remote deployments. URL entries are
// parsed before reaching go-redis so SentinelAddrs contain host:port values and
// TLSConfig is populated on FailoverOptions.
//
// ref: redis/go-redis sentinel.go ParseFailoverURL — FailoverOptions carries a
// single TLSConfig that enables TLS dials for Sentinel and the resolved master.
func buildFailoverOptions(cfg Config) (*goredis.FailoverOptions, error) {
	base := &goredis.FailoverOptions{
		MasterName:   cfg.SentinelMaster,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolSize:     cfg.PoolSize,
	}

	hasURL, hasPlain := sentinelAddressForms(cfg.SentinelAddrs)
	if hasURL && hasPlain {
		return nil, errcode.New(ErrAdapterRedisConnect,
			"redis: sentinel addresses cannot mix URL and host:port forms")
	}
	if !hasURL {
		base.SentinelAddrs = append([]string(nil), cfg.SentinelAddrs...)
		return base, nil
	}

	return buildFailoverURLOptions(base, cfg.SentinelAddrs)
}

func sentinelAddressForms(addrs []string) (hasURL, hasPlain bool) {
	for _, addr := range addrs {
		if strings.Contains(addr, "://") {
			hasURL = true
		} else {
			hasPlain = true
		}
	}
	return hasURL, hasPlain
}

func buildFailoverURLOptions(base *goredis.FailoverOptions, addrs []string) (*goredis.FailoverOptions, error) {
	for _, addr := range addrs {
		if err := appendFailoverURL(base, addr); err != nil {
			return nil, err
		}
	}
	return base, nil
}

func appendFailoverURL(base *goredis.FailoverOptions, addr string) error {
	parsed, err := goredis.ParseFailoverURL(addr)
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisConnect,
			fmt.Sprintf("redis: invalid SentinelAddrs URL %q", addr), err)
	}
	if len(base.SentinelAddrs) > 0 && ((base.TLSConfig == nil) != (parsed.TLSConfig == nil)) {
		return errcode.New(ErrAdapterRedisConnect,
			"redis: sentinel URL addresses must use the same TLS scheme")
	}
	if len(base.SentinelAddrs) == 0 {
		base.TLSConfig = failoverTLSConfig(parsed.TLSConfig)
	} else if err := checkFailoverTLSConfigCompatible(base.TLSConfig, parsed.TLSConfig); err != nil {
		return err
	}
	if err := mergeFailoverURLFields(base, parsed); err != nil {
		return err
	}
	if len(parsed.SentinelAddrs) != 1 {
		return errcode.New(ErrAdapterRedisConnect,
			fmt.Sprintf("redis: invalid SentinelAddrs URL %q", addr))
	}
	base.SentinelAddrs = append(base.SentinelAddrs, parsed.SentinelAddrs[0])
	return nil
}

func checkFailoverTLSConfigCompatible(base, parsed *tls.Config) error {
	if base == nil || parsed == nil || base.InsecureSkipVerify == parsed.InsecureSkipVerify {
		return nil
	}
	return errcode.New(ErrAdapterRedisConnect,
		"redis: sentinel URL addresses must use the same TLS verification settings")
}

func mergeFailoverURLFields(base, parsed *goredis.FailoverOptions) error {
	if err := mergeFailoverStringField(&base.MasterName, parsed.MasterName, "master_name"); err != nil {
		return err
	}
	if err := mergeFailoverStringField(&base.Username, parsed.Username, "username"); err != nil {
		return err
	}
	if err := mergeFailoverStringField(&base.Password, parsed.Password, "password"); err != nil {
		return err
	}
	if err := mergeFailoverStringField(&base.SentinelUsername, parsed.SentinelUsername, "sentinel username"); err != nil {
		return err
	}
	if err := mergeFailoverStringField(&base.SentinelPassword, parsed.SentinelPassword, "sentinel password"); err != nil {
		return err
	}
	return mergeFailoverIntField(&base.DB, parsed.DB, "db")
}

func mergeFailoverStringField(dst *string, incoming, name string) error {
	if incoming == "" || *dst == incoming {
		return nil
	}
	if *dst != "" {
		return errcode.New(ErrAdapterRedisConnect,
			fmt.Sprintf("redis: conflicting Sentinel URL %s values", name))
	}
	*dst = incoming
	return nil
}

func mergeFailoverIntField(dst *int, incoming int, name string) error {
	if incoming == 0 || *dst == incoming {
		return nil
	}
	if *dst != 0 {
		return errcode.New(ErrAdapterRedisConnect,
			fmt.Sprintf("redis: conflicting Sentinel URL %s values", name))
	}
	*dst = incoming
	return nil
}

func failoverTLSConfig(parsed *tls.Config) *tls.Config {
	if parsed == nil {
		return nil
	}
	cfg := parsed.Clone()
	// FailoverOptions carries a single TLSConfig shared by every Sentinel dial
	// and by the resolved master dial. Leaving the first URL's ServerName here
	// would force that SNI onto all later addresses; an empty ServerName lets
	// crypto/tls infer the host from each tls.DialWithDialer target.
	cfg.ServerName = ""
	return cfg
}

// Health pings the Redis server and returns an error if it is unreachable.
func (c *Client) Health(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return errcode.Wrap(ErrAdapterRedisConnect,
			fmt.Sprintf("redis: health check failed (addr=%s)", c.config.Addr), err)
	}
	return nil
}

// Close releases the underlying Redis connection, bounded by ctx.
//
// go-redis Client.Close() is synchronous and may block on in-flight commands.
// Close wraps it in a goroutine so the caller's shutdown budget is honoured;
// if ctx expires, in-flight commands may be abandoned (process-exit semantics).
//
// ref: uber-go/fx app.go StopTimeout — ctx as shared shutdown budget.
// ref: uber-go/fx lifecycle OnStop(ctx) — ContextCloser pattern.
func (c *Client) Close(ctx context.Context) error {
	return adapterutil.CloseWithDeadline(ctx, "redis", func() error {
		if err := c.rdb.Close(); err != nil {
			return errcode.Wrap(ErrAdapterRedisConnect, "redis: close failed", err)
		}
		return nil
	})
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
