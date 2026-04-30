package main

import (
	"bytes"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRejectDemoKey_DevMode_AlwaysPasses(t *testing.T) {
	for _, demo := range wellKnownDemoKeys {
		err := rejectDemoKey("", "X_TEST_ENV", []byte(demo))
		require.NoError(t, err, "dev mode must not reject demo key %q", demo)
	}
}

func TestRejectDemoKey_RealMode_RejectsEachDemoValue(t *testing.T) {
	for _, demo := range wellKnownDemoKeys {
		t.Run(demo, func(t *testing.T) {
			err := rejectDemoKey("real", "X_TEST_ENV", []byte(demo))
			require.Error(t, err, "real mode must reject demo key %q", demo)
			assert.Contains(t, err.Error(), "X_TEST_ENV")
			assert.Contains(t, err.Error(), "well-known demo key")
		})
	}
}

func TestRejectDemoKey_RealMode_AcceptsFreshSecret(t *testing.T) {
	fresh := bytes.Repeat([]byte("z"), 32)
	err := rejectDemoKey("real", "GOCELL_AUDITCORE_CURSOR_KEY", fresh)
	require.NoError(t, err, "real mode must accept a non-demo secret")
}

func TestRejectDemoKey_RealMode_EmptyKeyPasses(t *testing.T) {
	// Empty keys are handled upstream by loadSecret; rejectDemoKey must not
	// treat them as a demo match (len mismatch).
	err := rejectDemoKey("real", "GOCELL_AUDITCORE_CURSOR_KEY", nil)
	require.NoError(t, err)
}

// TestDevDefaults_AreAllInWellKnownDemoKeys guards against the pattern where
// a new dev-only default is added to loadSecret/buildCursorCodec call sites
// without being appended to wellKnownDemoKeys. Without this test, a stale
// dev default could silently pass rejectDemoKey in real mode.
func TestDevDefaults_AreAllInWellKnownDemoKeys(t *testing.T) {
	// The dev defaults currently wired in cell modules. Keep this list in sync
	// with loadSecret / buildCursorCodec call sites (audit_module.go,
	// config_module.go, access_module.go) when they change.
	// Note: GOCELL_SERVICE_SECRET has no dev-default; it is required in every
	// mode and rejectDemoKey is called directly in internalGuardFromEnv.
	devDefaults := []string{
		"dev-hmac-key-replace-in-prod!!!!", // buildHMACKey("GOCELL_AUDITCORE_HMAC_KEY", ...)
		"corebundle-audit-cursor-key-32b!", // buildCursorCodec(... LoadCursorKeys("AUDITCORE") ...)
		"corebundle-cfg-cursor-key--32bb!", // buildCursorCodec(... LoadCursorKeys("CONFIGCORE") ...)
	}
	for _, dd := range devDefaults {
		t.Run(dd, func(t *testing.T) {
			if slices.Contains(wellKnownDemoKeys, dd) {
				return
			}
			t.Errorf("dev default %q is not in wellKnownDemoKeys — real mode will silently accept it; add to demo_keys.go", dd)
		})
	}
}

// TestMasterKeyDemoHex_IsInWellKnownDemoKeys verifies that the test-fixture
// hex-encoded master key "0123456789abcdef..." is listed in wellKnownDemoKeys,
// so that rejectDemoKey blocks it in real adapter mode.
func TestMasterKeyDemoHex_IsInWellKnownDemoKeys(t *testing.T) {
	const demoHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if slices.Contains(wellKnownDemoKeys, demoHex) {
		return
	}
	t.Errorf("demo master key hex %q not found in wellKnownDemoKeys — real mode will accept it; add to demo_keys.go", demoHex)
}

// TestCellDemoKeys_AreAllInWellKnownDemoKeys guards against a cell being added
// with a new per-cell demo codec key (cells/*/cell.go) without the value also
// appearing in wellKnownDemoKeys. Without this guard, a cell that forgets
// WithCursorCodec in real mode would still sign cursors with a public key.
//
// Keep this list in sync with cells/*/cell.go initCursorCodec / Init paths.
func TestCellDemoKeys_AreAllInWellKnownDemoKeys(t *testing.T) {
	// Per-cell demo keys hard-coded in each cell's Init/initCursorCodec.
	// When a new cell is added with its own demo key, append here AND in
	// demo_keys.go wellKnownDemoKeys (append-only rule applies).
	cellDemoKeys := []string{
		"gocell-demo-AUDIT--CORE-key-32!!", // cells/auditcore/cell.go
		"gocell-demo-CONFIG-CORE-key-32!!", // cells/configcore/cell.go
		"gocell-demo-ORDER-CELL-key-32b!!", // examples/todoorder/cells/ordercell/cell.go
		"gocell-demo-DEVICE-CELL-key-32!!", // examples/iotdevice/cells/devicecell/cell.go
	}
	wellKnownSet := make(map[string]bool, len(wellKnownDemoKeys))
	for _, k := range wellKnownDemoKeys {
		wellKnownSet[k] = true
	}
	for _, ck := range cellDemoKeys {
		t.Run(ck, func(t *testing.T) {
			if !wellKnownSet[ck] {
				t.Errorf("cell demo key %q not in wellKnownDemoKeys — real mode will accept it; add to demo_keys.go", ck)
			}
		})
	}
}
