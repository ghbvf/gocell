// per_cell_adapter.go: per-cell env-loading helpers for adapter configuration.
//
// Each function reads a cell-namespaced env prefix so that adapter configuration
// is private to the owning module. No global env names are read here.
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
			return adapterpg.Config{}, fmt.Errorf("LoadPGConfig(%s): %sMAX_CONNS must be > 0, got %d (pgx treats 0 as unlimited)", cellEnvPrefix, prefix, n)
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

// LoadCursorKeys reads the cursor HMAC primary and previous keys for a cell.
//
// Env variables read:
//
//	GOCELL_<CELLID>_CURSOR_KEY
//	GOCELL_<CELLID>_CURSOR_PREVIOUS_KEY
//
// Either or both may be empty; callers are responsible for validation via
// buildCursorCodec.
func LoadCursorKeys(cellEnvPrefix string) (primary, previous string) {
	prefix := "GOCELL_" + cellEnvPrefix + "_CURSOR_"
	return os.Getenv(prefix + "KEY"), os.Getenv(prefix + "PREVIOUS_KEY")
}

// LoadConfigCoreKeyProvider reads the configcore-specific KeyProvider
// configuration:
//
//	GOCELL_CONFIGCORE_KEY_PROVIDER
//	GOCELL_CONFIGCORE_MASTER_KEY
//	GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS
//
// Returns (providerName, masterKey, prevMasterKey). Any or all may be empty;
// buildKeyProvider validates the values before constructing a KeyProvider.
func LoadConfigCoreKeyProvider() (providerName, masterKey, prevMasterKey string) {
	return os.Getenv("GOCELL_CONFIGCORE_KEY_PROVIDER"),
		os.Getenv("GOCELL_CONFIGCORE_MASTER_KEY"),
		os.Getenv("GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS")
}

// LoadCellHMACKey reads the HMAC key env var for a specific cell using the
// per-cell namespace convention (GOCELL_<CELLID>_HMAC_KEY). The returned value
// is the raw env string — callers pass it to buildHMACKey for dev-fallback,
// demo-key rejection, and real-mode fail-fast validation.
//
// cellEnvPrefix is the uppercase cell id (e.g. "AUDITCORE").
//
// ref: Kratos config/env prefix-strip convention.
func LoadCellHMACKey(cellEnvPrefix string) string {
	return os.Getenv("GOCELL_" + cellEnvPrefix + "_HMAC_KEY")
}
