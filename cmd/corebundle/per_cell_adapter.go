// per_cell_adapter.go: per-cell env-loading helpers for adapter configuration.
//
// ref: Kratos config/env prefix-strip convention — each module reads its own namespace.
// ref: uber-go/fx fx.Module + fx.Private — module-private dependencies.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
)

// LoadPGConfig constructs a postgres pool Config for the given cell by reading
// cell-namespaced environment variables:
//
//	GOCELL_<CELLID>_DATABASE_URL
//	GOCELL_<CELLID>_DATABASE_MAX_CONNS
//	GOCELL_<CELLID>_DATABASE_IDLE_TIMEOUT
//	GOCELL_<CELLID>_DATABASE_MAX_LIFETIME
//
// Invalid int/duration values and non-positive MAX_CONNS return an error; the
// message includes the env var name and the actual value so operators can
// diagnose misconfiguration at startup. DSN may be empty; callers must
// validate it before calling NewPool in postgres mode.
//
// ref: Kratos config/env prefix-strip convention.
// ref: uber-go/fx fx.Module + fx.Private — module-private configuration.
func LoadPGConfig(cellEnvPrefix string) (adapterpg.Config, error) {
	prefix := "GOCELL_" + cellEnvPrefix + "_DATABASE_"

	cfg := adapterpg.Config{
		DSN: os.Getenv(prefix + "URL"),
	}

	if v := os.Getenv(prefix + "MAX_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			return adapterpg.Config{}, fmt.Errorf("LoadPGConfig(%s): invalid %sMAX_CONNS %q: %w", cellEnvPrefix, prefix, v, err)
		}
		if n <= 0 {
			return adapterpg.Config{}, fmt.Errorf(
				"LoadPGConfig(%s): %sMAX_CONNS must be > 0, got %d (pgx treats 0 as unlimited)",
				cellEnvPrefix, prefix, n)
		}
		cfg.MaxConns = int32(n)
	}
	if v := os.Getenv(prefix + "IDLE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return adapterpg.Config{}, fmt.Errorf("LoadPGConfig(%s): invalid %sIDLE_TIMEOUT %q: %w", cellEnvPrefix, prefix, v, err)
		}
		cfg.IdleTimeout = d
	}
	if v := os.Getenv(prefix + "MAX_LIFETIME"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return adapterpg.Config{}, fmt.Errorf("LoadPGConfig(%s): invalid %sMAX_LIFETIME %q: %w", cellEnvPrefix, prefix, v, err)
		}
		cfg.MaxLifetime = d
	}

	return cfg, nil
}
