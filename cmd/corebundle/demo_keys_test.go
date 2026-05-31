package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
)

func TestRejectDemoKey_DevMode_AlwaysPasses(t *testing.T) {
	for _, demo := range cellsecrets.WellKnownDemoKeys() {
		err := cellsecrets.RejectDemoKey("", "X_TEST_ENV", []byte(demo))
		require.NoError(t, err, "dev mode must not reject demo key %q", demo)
	}
}

func TestRejectDemoKey_RealMode_RejectsEachDemoValue(t *testing.T) {
	for _, demo := range cellsecrets.WellKnownDemoKeys() {
		t.Run(demo, func(t *testing.T) {
			err := cellsecrets.RejectDemoKey("real", "X_TEST_ENV", []byte(demo))
			require.Error(t, err, "real mode must reject demo key %q", demo)
			assert.Contains(t, err.Error(), "X_TEST_ENV")
			assert.Contains(t, err.Error(), "well-known demo key")
		})
	}
}

func TestRejectDemoKey_RealMode_AcceptsFreshSecret(t *testing.T) {
	fresh := bytes.Repeat([]byte("z"), 32)
	err := cellsecrets.RejectDemoKey("real", "GOCELL_AUDITCORE_CURSOR_KEY", fresh)
	require.NoError(t, err, "real mode must accept a non-demo secret")
}

func TestRejectDemoKey_RealMode_EmptyKeyPasses(t *testing.T) {
	// Empty keys are handled upstream by loadSecret; rejectDemoKey must not
	// treat them as a demo match (len mismatch).
	err := cellsecrets.RejectDemoKey("real", "GOCELL_AUDITCORE_CURSOR_KEY", nil)
	require.NoError(t, err)
}

// TestDevDefaults_AreAllInWellKnownDemoKeys guards against the pattern where
// a new dev-only default is added to platform cell module call sites without
// being appended to cellsecrets.WellKnownDemoKeys.
func TestDevDefaults_AreAllInWellKnownDemoKeys(t *testing.T) {
	devDefaults := []string{
		"dev-hmac-key-replace-in-prod!!!!", // buildAuditProtocol("GOCELL_AUDITCORE_HMAC_KEY", ...)
		"corebundle-audit-cursor-key-32b!", // cellmodules/auditcore BuildCursorCodec(AUDITCORE)
		"corebundle-cfg-cursor-key--32bb!", // cellmodules/configcore BuildCursorCodec(CONFIGCORE)
	}
	for _, dd := range devDefaults {
		t.Run(dd, func(t *testing.T) {
			if cellsecrets.IsWellKnownDemoKey([]byte(dd)) {
				return
			}
			t.Errorf("dev default %q is not in WellKnownDemoKeys — real mode will silently accept it; add to cellsecrets", dd)
		})
	}
}

// TestMasterKeyDemoHex_IsInWellKnownDemoKeys verifies that the test-fixture
// hex-encoded master key is listed in WellKnownDemoKeys.
func TestMasterKeyDemoHex_IsInWellKnownDemoKeys(t *testing.T) {
	const demoHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if cellsecrets.IsWellKnownDemoKey([]byte(demoHex)) {
		return
	}
	t.Errorf("demo master key hex %q not found in WellKnownDemoKeys — real mode will accept it; add to cellsecrets", demoHex)
}

// TestCellDemoKeys_AreAllInWellKnownDemoKeys guards against a cell being added
// with a new per-cell demo codec key without the value also appearing in
// cellsecrets.WellKnownDemoKeys.
func TestCellDemoKeys_AreAllInWellKnownDemoKeys(t *testing.T) {
	cellDemoKeys := []string{
		"gocell-demo-AUDIT--CORE-key-32!!", // cells/auditcore/cell.go
		"gocell-demo-CONFIG-CORE-key-32!!", // cells/configcore/cell.go
		"gocell-demo-ORDER-CELL-key-32b!!", // examples/todoorder/cells/ordercell/cell.go
		"gocell-demo-DEVICE-CELL-key-32!!", // examples/iotdevice/cells/devicecell/cell.go
	}
	demoKeys := cellsecrets.WellKnownDemoKeys()
	wellKnownSet := make(map[string]bool, len(demoKeys))
	for _, k := range demoKeys {
		wellKnownSet[k] = true
	}
	for _, ck := range cellDemoKeys {
		t.Run(ck, func(t *testing.T) {
			if !wellKnownSet[ck] {
				t.Errorf("cell demo key %q not in WellKnownDemoKeys — real mode will accept it; add to cellsecrets", ck)
			}
		})
	}
}
