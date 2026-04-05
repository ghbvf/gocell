package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// PoolConfig holds the PostgreSQL connection pool settings.
type PoolConfig struct {
	DSN             string
	MaxConns        int
	MinConns        int
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// DefaultPoolConfig returns a PoolConfig with sensible defaults.
func DefaultPoolConfig(dsn string) PoolConfig {
	return PoolConfig{
		DSN:             dsn,
		MaxConns:        10,
		MinConns:        2,
		MaxConnLifetime: 30 * time.Minute,
		MaxConnIdleTime: 5 * time.Minute,
	}
}

// Pool wraps a database connection pool. In production this wraps pgxpool.Pool;
// this stub provides the interface for higher-level adapter code.
type Pool struct {
	dsn    string
	config PoolConfig
}

// NewPool creates a new Pool. It does NOT open a connection yet; call
// Connect() to establish the pool.
func NewPool(cfg PoolConfig) *Pool {
	return &Pool{
		dsn:    cfg.DSN,
		config: cfg,
	}
}

// Connect establishes the connection pool. In the stub implementation this
// validates configuration only; a real implementation would call pgxpool.New().
func (p *Pool) Connect(ctx context.Context) error {
	if p.dsn == "" {
		return errcode.New(ErrAdapterPGConnect, "postgres: DSN is empty")
	}
	slog.Info("postgres: pool connected",
		slog.String("dsn", sanitizeDSN(p.dsn)),
		slog.Int("max_conns", p.config.MaxConns),
	)
	return nil
}

// Close shuts down the connection pool.
func (p *Pool) Close() {
	slog.Info("postgres: pool closed")
}

// Health checks the connection pool liveness.
func (p *Pool) Health(ctx context.Context) error {
	if p.dsn == "" {
		return errcode.New(ErrAdapterPGConnect, "postgres: pool not connected")
	}
	return nil
}

// txKey is the context key for propagating a transaction.
type txKey struct{}

// ContextWithTx stores a Tx in the context for downstream use.
func ContextWithTx(ctx context.Context, tx Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFromContext extracts a Tx from the context. Returns nil if absent.
func TxFromContext(ctx context.Context) Tx {
	tx, _ := ctx.Value(txKey{}).(Tx)
	return tx
}

// Tx abstracts a database transaction to avoid direct pgx dependency.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Rows abstracts a result set.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// Row abstracts a single-row result.
type Row interface {
	Scan(dest ...any) error
}

// DBTX abstracts both Pool and Tx for repository use.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// sanitizeDSN removes the password from a DSN for safe logging.
func sanitizeDSN(dsn string) string {
	if len(dsn) > 20 {
		return dsn[:20] + "***"
	}
	return fmt.Sprintf("<%d chars>", len(dsn))
}
