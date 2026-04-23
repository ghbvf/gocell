// per_cell_adapter.go: per-cell env-loading helpers for adapter configuration.
//
// Each function reads a cell-namespaced env prefix so that adapter configuration
// is private to the owning module. No global env names are read here.
//
// ref: Kratos config/env prefix-strip convention — each module reads its own namespace.
// ref: uber-go/fx fx.Module + fx.Private — module-private dependencies.
package main

import (
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
// Invalid int/duration values are silently ignored (field left at zero).
// Zero-valued numeric fields are filled by adapterpg.Config.applyDefaults when
// adapterpg.NewPool is called. DSN may be empty; callers must validate it
// before calling NewPool in postgres mode.
//
// ref: Kratos config/env prefix-strip convention.
// ref: uber-go/fx fx.Module + fx.Private — module-private configuration.
func LoadPGConfig(cellEnvPrefix string) adapterpg.Config {
	prefix := "GOCELL_" + cellEnvPrefix + "_DATABASE_"

	cfg := adapterpg.Config{
		DSN: os.Getenv(prefix + "URL"),
	}

	if v := os.Getenv(prefix + "MAX_CONNS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			cfg.MaxConns = int32(n)
		}
	}
	if v := os.Getenv(prefix + "IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.IdleTimeout = d
		}
	}
	if v := os.Getenv(prefix + "MAX_LIFETIME"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxLifetime = d
		}
	}

	return cfg
}

// LoadCursorKeys reads the cursor HMAC primary and previous keys for a cell.
//
// Env variables read:
//
//	GOCELL_<CELLID>_CURSOR_KEY
//	GOCELL_<CELLID>_CURSOR_PREVIOUS_KEY
//
// Either or both may be empty; callers are responsible for validation via
// loadCursorCodec.
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

// LoadAuditCoreHMACKey reads GOCELL_AUDITCORE_HMAC_KEY and returns its raw
// string value. An empty value means the key is not configured.
func LoadAuditCoreHMACKey() string {
	return os.Getenv("GOCELL_AUDITCORE_HMAC_KEY")
}
