package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Default pool configuration values.
const (
	defaultMaxConns     = 10
	defaultIdleTimeout  = 5 * time.Minute
	defaultMaxLifetime  = 1 * time.Hour
	defaultHealthTimeout = 5 * time.Second
)

// Config holds PostgreSQL connection pool settings.
// Fields are populated from an explicit struct literal or from environment
// variables via ConfigFromEnv.
type Config struct {
	// DSN is the PostgreSQL connection string (e.g.
	// "postgres://user:pass@localhost:5432/dbname?sslmode=disable").
	// When empty, ConfigFromEnv reads GOCELL_PG_DSN.
	DSN string

	// MaxConns is the maximum number of connections in the pool.
	// Default: 10. Env: GOCELL_PG_MAX_CONNS.
	MaxConns int32

	// IdleTimeout is how long an idle connection may remain in the pool.
	// Default: 5m. Env: GOCELL_PG_IDLE_TIMEOUT (duration string).
	IdleTimeout time.Duration

	// MaxLifetime is the maximum lifetime of a connection.
	// Default: 1h. Env: GOCELL_PG_MAX_LIFETIME (duration string).
	MaxLifetime time.Duration
}

// ConfigFromEnv builds a Config from environment variables.
// Missing or unparseable values fall back to defaults.
func ConfigFromEnv() Config {
	cfg := Config{
		DSN:         os.Getenv("GOCELL_PG_DSN"),
		MaxConns:    defaultMaxConns,
		IdleTimeout: defaultIdleTimeout,
		MaxLifetime: defaultMaxLifetime,
	}

	if v := os.Getenv("GOCELL_PG_MAX_CONNS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			cfg.MaxConns = int32(n)
		}
	}
	if v := os.Getenv("GOCELL_PG_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.IdleTimeout = d
		}
	}
	if v := os.Getenv("GOCELL_PG_MAX_LIFETIME"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxLifetime = d
		}
	}
	return cfg
}

// applyDefaults fills zero-valued fields with default values.
func (c *Config) applyDefaults() {
	if c.MaxConns <= 0 {
		c.MaxConns = defaultMaxConns
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.MaxLifetime <= 0 {
		c.MaxLifetime = defaultMaxLifetime
	}
}

// Pool wraps a pgxpool.Pool with health checking and lifecycle management.
type Pool struct {
	inner  *pgxpool.Pool
	config Config
}

// NewPool creates a new connection pool from the supplied Config.
// It validates the DSN, applies defaults, and pings the database to confirm
// connectivity.
func NewPool(ctx context.Context, cfg Config) (*Pool, error) {
	cfg.applyDefaults()

	if cfg.DSN == "" {
		return nil, errcode.New(ErrAdapterPGConnect, "postgres DSN is empty")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "postgres: parse DSN", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MaxConnIdleTime = cfg.IdleTimeout
	poolCfg.MaxConnLifetime = cfg.MaxLifetime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "postgres: create pool", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errcode.Wrap(ErrAdapterPGConnect, "postgres: initial ping", err)
	}

	slog.Info("postgres pool connected",
		slog.String("host", poolCfg.ConnConfig.Host),
		slog.Int("max_conns", int(cfg.MaxConns)),
	)

	return &Pool{inner: pool, config: cfg}, nil
}

// DB returns the underlying pgxpool.Pool for direct access.
func (p *Pool) DB() *pgxpool.Pool {
	return p.inner
}

// Health performs a ping against the database and returns nil if healthy.
func (p *Pool) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, defaultHealthTimeout)
	defer cancel()

	if err := p.inner.Ping(ctx); err != nil {
		return errcode.Wrap(ErrAdapterPGConnect, "postgres: health check failed", err)
	}
	return nil
}

// Close gracefully shuts down the connection pool.
func (p *Pool) Close() {
	p.inner.Close()
	slog.Info("postgres pool closed")
}

// PoolStats holds structured connection pool statistics.
//
// ref: pgxpool Stat() — adopted same field set for operational dashboards
// and Prometheus/OTel metric collectors.
type PoolStats struct {
	AcquireCount            int64         `json:"acquireCount"`
	AcquireDuration         time.Duration `json:"acquireDuration"`
	AcquiredConns           int32         `json:"acquiredConns"`
	CanceledAcquireCount    int64         `json:"canceledAcquireCount"`
	ConstructingConns       int32         `json:"constructingConns"`
	EmptyAcquireCount       int64         `json:"emptyAcquireCount"`
	IdleConns               int32         `json:"idleConns"`
	MaxConns                int32         `json:"maxConns"`
	TotalConns              int32         `json:"totalConns"`
	NewConnsCount           int64         `json:"newConnsCount"`
	MaxLifetimeDestroyCount int64         `json:"maxLifetimeDestroyCount"`
	MaxIdleDestroyCount     int64         `json:"maxIdleDestroyCount"`
}

// PoolStats returns structured pool statistics suitable for metrics collection
// and operational dashboards. Returns zero-value PoolStats if the pool is not
// initialized (defensive guard, consistent with Redis adapter pattern).
func (p *Pool) PoolStats() PoolStats {
	if p.inner == nil {
		return PoolStats{}
	}
	s := p.inner.Stat()
	return PoolStats{
		AcquireCount:            s.AcquireCount(),
		AcquireDuration:         s.AcquireDuration(),
		AcquiredConns:           s.AcquiredConns(),
		CanceledAcquireCount:    s.CanceledAcquireCount(),
		ConstructingConns:       s.ConstructingConns(),
		EmptyAcquireCount:       s.EmptyAcquireCount(),
		IdleConns:               s.IdleConns(),
		MaxConns:                s.MaxConns(),
		TotalConns:              s.TotalConns(),
		NewConnsCount:           s.NewConnsCount(),
		MaxLifetimeDestroyCount: s.MaxLifetimeDestroyCount(),
		MaxIdleDestroyCount:     s.MaxIdleDestroyCount(),
	}
}

// Stats returns pool statistics as a formatted string for diagnostics.
func (p *Pool) Stats() string {
	s := p.inner.Stat()
	return fmt.Sprintf(
		"total=%d idle=%d acquired=%d constructing=%d max=%d",
		s.TotalConns(), s.IdleConns(), s.AcquiredConns(),
		s.ConstructingConns(), s.MaxConns(),
	)
}
