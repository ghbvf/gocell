// Package postgres wires PostgreSQL-backed repositories for configcore.
//
// This package is intentionally outside cells/configcore's root package so the
// Cell's exported API stays port-oriented while composition roots can still
// choose the concrete storage adapter.
package postgres

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	configcore "github.com/ghbvf/gocell/cells/configcore"
	cellpg "github.com/ghbvf/gocell/cells/configcore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
)

type settings struct {
	transformer   kcrypto.ValueTransformer
	logger        *slog.Logger
	onStaleCipher func(key, storedKeyID, currentKeyID string)
}

// Option configures PostgreSQL repository wiring for configcore.
type Option func(*settings)

// WithValueTransformer sets the transformer used by the config repository to
// encrypt/decrypt sensitive values at the repository boundary.
func WithValueTransformer(t kcrypto.ValueTransformer) Option {
	return func(s *settings) { s.transformer = t }
}

// WithLogger sets the logger used by the config repository. Nil means
// slog.Default(), matching the repository constructor's default.
func WithLogger(l *slog.Logger) Option {
	return func(s *settings) { s.logger = l }
}

// WithOnStaleCipher sets the callback invoked when a stale encrypted value is
// read. Composition roots commonly use this to increment an observability
// counter without exposing Prometheus types through cells/configcore.
func WithOnStaleCipher(fn func(key, storedKeyID, currentKeyID string)) Option {
	return func(s *settings) { s.onStaleCipher = fn }
}

// WithPool injects PostgreSQL-backed config and flag repositories into
// configcore using the provided pool.
//
// clk must be non-nil; pass clock.Real() at the composition root.
// The pool lifecycle, schema guard, TxManager, outbox writer, and relay remain
// the composition root's responsibility. This option only adapts the pool into
// configcore's cell-local repository ports.
//
// Returns ErrCellInvalidConfig when pool is nil so wiring mistakes fail before
// ConfigCore is constructed with unusable repositories.
//
// WithLogger configures repository logs only. Call configcore.WithLogger
// separately when the cell itself should use the same logger.
func WithPool(pool *pgxpool.Pool, clk clock.Clock, opts ...Option) (configcore.Option, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"configcore/postgres: WithPool requires a non-nil *pgxpool.Pool")
	}
	clock.MustHaveClock(clk, "configcore/postgres.WithPool")
	cfg := settings{logger: slog.Default()}
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(c *configcore.ConfigCore) {
		session := cellpg.NewSession(pool)
		var repoOpts []cellpg.ConfigRepoOption
		if cfg.onStaleCipher != nil {
			repoOpts = append(repoOpts, cellpg.WithOnStaleCipher(cfg.onStaleCipher))
		}
		configcore.WithConfigRepository(cellpg.NewConfigRepository(session, cfg.transformer, cfg.logger, clk, repoOpts...))(c)
		configcore.WithFlagRepository(cellpg.NewFlagRepository(session, clk))(c)
	}, nil
}
