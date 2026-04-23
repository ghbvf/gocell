// per_cell_adapter_test.go: table-driven tests for per-cell env loading helpers.
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TestLoadPGConfig
// ---------------------------------------------------------------------------

func TestLoadPGConfig_AllEmpty_ReturnsZeroDSN(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", "")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_CONNS", "")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_IDLE_TIMEOUT", "")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_LIFETIME", "")

	cfg := LoadPGConfig("CONFIGCORE")
	assert.Equal(t, "", cfg.DSN, "empty env must produce empty DSN")
	// MaxConns/IdleTimeout/MaxLifetime are zero here; applyDefaults fills them at NewPool time.
	assert.EqualValues(t, 0, cfg.MaxConns)
	assert.Equal(t, time.Duration(0), cfg.IdleTimeout)
	assert.Equal(t, time.Duration(0), cfg.MaxLifetime)
}

func TestLoadPGConfig_OnlyDSN_CorrectlyRead(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", "postgres://cfg:pass@localhost/db")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_CONNS", "")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_IDLE_TIMEOUT", "")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_LIFETIME", "")

	cfg := LoadPGConfig("CONFIGCORE")
	assert.Equal(t, "postgres://cfg:pass@localhost/db", cfg.DSN)
}

func TestLoadPGConfig_AllFields_ParsedCorrectly(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", "postgres://cfg:x@host/db")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_CONNS", "20")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_IDLE_TIMEOUT", "2m")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_LIFETIME", "30m")

	cfg := LoadPGConfig("CONFIGCORE")
	assert.Equal(t, "postgres://cfg:x@host/db", cfg.DSN)
	assert.EqualValues(t, 20, cfg.MaxConns)
	assert.Equal(t, 2*time.Minute, cfg.IdleTimeout)
	assert.Equal(t, 30*time.Minute, cfg.MaxLifetime)
}

func TestLoadPGConfig_InvalidDuration_FallsBackToZero(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", "postgres://x/db")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_CONNS", "not-a-number")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_IDLE_TIMEOUT", "bad-duration")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_LIFETIME", "bad-duration")

	cfg := LoadPGConfig("CONFIGCORE")
	assert.Equal(t, "postgres://x/db", cfg.DSN)
	// Invalid int/duration: stays 0 (applyDefaults fills in at NewPool time).
	assert.EqualValues(t, 0, cfg.MaxConns)
	assert.Equal(t, time.Duration(0), cfg.IdleTimeout)
	assert.Equal(t, time.Duration(0), cfg.MaxLifetime)
}

func TestLoadPGConfig_CrossCellIsolation(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", "postgres://cfg/db")
	t.Setenv("GOCELL_ACCESSCORE_DATABASE_URL", "")

	cfgCore := LoadPGConfig("CONFIGCORE")
	accessCore := LoadPGConfig("ACCESSCORE")

	assert.Equal(t, "postgres://cfg/db", cfgCore.DSN)
	assert.Equal(t, "", accessCore.DSN, "ACCESSCORE prefix must not pick up CONFIGCORE env")
}

// ---------------------------------------------------------------------------
// TestLoadCursorKeys
// ---------------------------------------------------------------------------

func TestLoadCursorKeys_PrimaryOnly(t *testing.T) {
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "audit-primary-key-value-here-32!")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY", "")

	primary, previous := LoadCursorKeys("AUDITCORE")
	assert.Equal(t, "audit-primary-key-value-here-32!", primary)
	assert.Equal(t, "", previous)
}

func TestLoadCursorKeys_PreviousOnly(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", "")
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_PREVIOUS_KEY", "config-prev-key-value-here-32!!")

	primary, previous := LoadCursorKeys("CONFIGCORE")
	assert.Equal(t, "", primary)
	assert.Equal(t, "config-prev-key-value-here-32!!", previous)
}

func TestLoadCursorKeys_BothSet(t *testing.T) {
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", "access-cur-key-value-here-32-b!")
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY", "access-prev-key-here-32-bytes!!")

	primary, previous := LoadCursorKeys("ACCESSCORE")
	assert.Equal(t, "access-cur-key-value-here-32-b!", primary)
	assert.Equal(t, "access-prev-key-here-32-bytes!!", previous)
}

func TestLoadCursorKeys_CrossCellIsolation(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", "config-cursor-set-here-value-32!")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY", "")

	primary, _ := LoadCursorKeys("AUDITCORE")
	require.Equal(t, "", primary, "AUDITCORE prefix must not pick up CONFIGCORE cursor key")
}

// ---------------------------------------------------------------------------
// TestLoadConfigCoreKeyProvider
// ---------------------------------------------------------------------------

func TestLoadConfigCoreKeyProvider_AllSet(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_KEY_PROVIDER", "local-aes")
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY", "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS", "prev-master-key-hex-placeholder!")

	providerName, masterKey, prevMasterKey := LoadConfigCoreKeyProvider()
	assert.Equal(t, "local-aes", providerName)
	assert.Equal(t, "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", masterKey)
	assert.Equal(t, "prev-master-key-hex-placeholder!", prevMasterKey)
}

func TestLoadConfigCoreKeyProvider_ProviderOnly(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_KEY_PROVIDER", "vault-transit")
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY", "")
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS", "")

	providerName, masterKey, prevMasterKey := LoadConfigCoreKeyProvider()
	assert.Equal(t, "vault-transit", providerName)
	assert.Equal(t, "", masterKey)
	assert.Equal(t, "", prevMasterKey)
}

func TestLoadConfigCoreKeyProvider_AllEmpty(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_KEY_PROVIDER", "")
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY", "")
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS", "")

	providerName, masterKey, prevMasterKey := LoadConfigCoreKeyProvider()
	assert.Equal(t, "", providerName)
	assert.Equal(t, "", masterKey)
	assert.Equal(t, "", prevMasterKey)
}

// ---------------------------------------------------------------------------
// TestLoadAuditCoreHMACKey
// ---------------------------------------------------------------------------

func TestLoadAuditCoreHMACKey_ReturnsValue(t *testing.T) {
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "audit-hmac-key-value-here-32!!!!")

	key := LoadAuditCoreHMACKey()
	assert.Equal(t, "audit-hmac-key-value-here-32!!!!", key)
}

func TestLoadAuditCoreHMACKey_EmptyReturnsEmpty(t *testing.T) {
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "")

	key := LoadAuditCoreHMACKey()
	assert.Equal(t, "", key)
}
