package auth

// percell_keyring_test.go — security behavior of per-cell service-token keying
// (#2153). The core property under test: in a split deployment a process that
// does NOT hold a cell's subkey cannot forge that cell's caller identity.

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

const (
	percellMethod = http.MethodGet
	percellPath   = "/internal/v1/percell"
)

var percellTime = time.Unix(1_700_000_000, 0)

// signAs produces a token claiming callerCell using ring. Empty result = the
// keyring refused to sign as callerCell (e.g. split: not its own cell).
func signAs(ring kauth.ServiceKeyring, callerCell string) string {
	return GenerateServiceToken(ring, callerCell, percellMethod, percellPath, "", tenant.TenantID(""), "", percellTime)
}

// verifyTokenWith reconstructs the MAC message from the token (as the verify
// path does) and checks it against ring's verify subkeys for the claimed caller.
func verifyTokenWith(t *testing.T, ring kauth.ServiceKeyring, token string) bool {
	t.Helper()
	parts := strings.SplitN(token, ":", 4)
	require.Len(t, parts, 4, "token must be 4-part")
	ts, nonce, caller, sigHex := parts[0], parts[1], parts[2], parts[3]
	mac, err := hex.DecodeString(sigHex)
	require.NoError(t, err)
	msg := buildServiceTokenMessage(percellMethod, percellPath, "", ts, nonce, caller, "", "")
	return verifyServiceTokenMAC(ring, caller, msg, mac)
}

// buildProvisioned derives ownCell's split key material from master (via the
// same DeriveProvisionedKeys the CLI uses) and assembles a master-absent
// ProvisionedKeyring scoped to the given declared callers.
func buildProvisioned(t *testing.T, master *HMACKeyRing, ownCell string, callers ...string) *ProvisionedKeyring {
	t.Helper()
	pk, err := DeriveProvisionedKeys(master, ownCell, callers)
	require.NoError(t, err)
	signing := [][]byte{pk.SigningCurrent}
	if pk.SigningPrevious != nil {
		signing = append(signing, pk.SigningPrevious)
	}
	verify := map[string][][]byte{}
	for c, cur := range pk.VerifyCurrent {
		s := [][]byte{cur}
		if prev, ok := pk.VerifyPrevious[c]; ok {
			s = append(s, prev)
		}
		verify[c] = s
	}
	ring, err := NewProvisionedKeyring(ownCell, signing, verify)
	require.NoError(t, err)
	return ring
}

func TestDeriveCellSecret_DeterministicAndDistinct(t *testing.T) {
	parent := []byte("0123456789abcdef0123456789abcdef")

	a1, err := deriveCellSecret(parent, "accesscore")
	require.NoError(t, err)
	a2, err := deriveCellSecret(parent, "accesscore")
	require.NoError(t, err)
	b, err := deriveCellSecret(parent, "auditcore")
	require.NoError(t, err)

	assert.Equal(t, a1, a2, "same parent+cell derives deterministically")
	assert.NotEqual(t, a1, b, "different cells derive independent subkeys")
	assert.Len(t, a1, MinHMACKeyBytes, "subkey is HMAC-SHA256 strength")
	assert.NotEqual(t, parent, a1, "subkey is not the raw parent")
}

// TestProvisioned_CrossCellForgery_FailsClosed is the #2153 Hard assertion: a
// split cell holding only its own signing subkey cannot mint a token claiming a
// different cell (sign side), and a callee holding verify subkeys only for its
// declared callers rejects tokens claiming any other cell (verify side).
func TestProvisioned_CrossCellForgery_FailsClosed(t *testing.T) {
	master := mustTestRing(t, testHMACKey, "")

	// accesscore process: master-absent, can sign only as accesscore.
	accesscore := buildProvisioned(t, master, "accesscore")
	// configcore process (callee): master-absent, verifies only accesscore.
	configcore := buildProvisioned(t, master, "configcore", "accesscore")

	// Sign side: accesscore can sign as itself, but NOT as another cell.
	require.NotEmpty(t, signAs(accesscore, "accesscore"), "may sign as own cell")
	assert.Empty(t, signAs(accesscore, "auditcore"),
		"a master-absent cell cannot mint another cell's token (sign-side fail-closed)")

	// Verify side: a legitimate accesscore token is accepted by configcore...
	tok := signAs(accesscore, "accesscore")
	assert.True(t, verifyTokenWith(t, configcore, tok), "declared caller accepted")

	// ...but a token claiming auditcore (configcore has no auditcore subkey) is
	// rejected — least-privilege verify, not a silent empty key.
	_, err := configcore.VerifySecrets("auditcore")
	require.Error(t, err, "verify subkey for undeclared caller must be absent")
	forged := signAs(master, "auditcore") // even a *valid* auditcore token...
	require.NotEmpty(t, forged)
	assert.False(t, verifyTokenWith(t, configcore, forged),
		"callee rejects callers outside its declared set (verify-side fail-closed)")
}

// TestProvisioned_SignOnlyAsOwnCell asserts the signing-side identity binding.
func TestProvisioned_SignOnlyAsOwnCell(t *testing.T) {
	master := mustTestRing(t, testHMACKey, "")
	accesscore := buildProvisioned(t, master, "accesscore")

	_, err := accesscore.SigningSecrets("accesscore")
	require.NoError(t, err)
	_, err = accesscore.SigningSecrets("configcore")
	require.Error(t, err, "a split cell may sign only as its own identity")
}

// TestProvisioned_InteropsWithMaster proves CLI-derived split keys are
// byte-identical to in-process master derivation: tokens cross-verify between a
// master (monolith) keyring and a ProvisionedKeyring built from
// DeriveProvisionedKeys. A drift here would silently break split↔monolith.
func TestProvisioned_InteropsWithMaster(t *testing.T) {
	master := mustTestRing(t, testHMACKey, "")
	accesscore := buildProvisioned(t, master, "accesscore")
	configcore := buildProvisioned(t, master, "configcore", "accesscore")

	// provisioned-signed token verifies under the master (monolith callee).
	tokFromSplit := signAs(accesscore, "accesscore")
	assert.True(t, verifyTokenWith(t, master, tokFromSplit),
		"provisioned-signed token verifies under master derivation")

	// master-signed token verifies under the provisioned callee.
	tokFromMaster := signAs(master, "accesscore")
	assert.True(t, verifyTokenWith(t, configcore, tokFromMaster),
		"master-signed token verifies under provisioned callee")
}

// TestProvisioned_Rotation: a token signed with the previous-generation subkey
// still verifies during a master-rotation overlap window.
func TestProvisioned_Rotation(t *testing.T) {
	oldMaster := mustTestRing(t, testHMACKeyOld, "")
	newMaster := mustTestRing(t, testHMACKeyNew, testHMACKeyOld)

	// accesscore signs with the OLD generation; configcore is provisioned from
	// the NEW master (current=new, previous=old) and must still verify.
	accesscoreOld := buildProvisioned(t, oldMaster, "accesscore")
	configcoreNew := buildProvisioned(t, newMaster, "configcore", "accesscore")

	tok := signAs(accesscoreOld, "accesscore")
	assert.True(t, verifyTokenWith(t, configcoreNew, tok),
		"previous-generation subkey verifies in rotation overlap window")
}

// TestMaster_MonolithIsNotHard documents (and pins) the explicit non-Hard
// property of the monolith keyring: a process holding the master can sign as ANY
// cell. Per-cell isolation requires master ABSENCE (ProvisionedKeyring) — this
// is why scope A (HKDF over a shared master) was rejected.
func TestMaster_MonolithIsNotHard(t *testing.T) {
	master := mustTestRing(t, testHMACKey, "")
	for _, cell := range []string{"accesscore", "auditcore", "configcore"} {
		assert.NotEmpty(t, signAs(master, cell),
			"master holder can sign as any cell (monolith is single trust domain, not per-cell-Hard)")
	}
}

func TestProvisionedKeyring_Validate_RejectsShortAndBadCell(t *testing.T) {
	good := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	short := []byte("short")

	_, err := NewProvisionedKeyring("accesscore", [][]byte{short}, nil)
	require.Error(t, err, "short signing subkey rejected")

	_, err = NewProvisionedKeyring("Bad-Cell", [][]byte{good}, nil)
	require.Error(t, err, "invalid cell id rejected")

	_, err = NewProvisionedKeyring("accesscore", [][]byte{good},
		map[string][][]byte{"auditcore": {short}})
	require.Error(t, err, "short verify subkey rejected")

	r, err := NewProvisionedKeyring("accesscore", [][]byte{good},
		map[string][][]byte{"auditcore": {good}})
	require.NoError(t, err)
	require.NoError(t, r.Validate())
}
