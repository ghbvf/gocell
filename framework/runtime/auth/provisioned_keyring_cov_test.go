package auth

// provisioned_keyring_cov_test.go — direct unit coverage for the per-cell keyring
// env load / format / derive error paths (#2153). These functions are exercised
// end-to-end from cmd (LoadProvisionedKeyringFromEnv via buildInternalServiceKeyring;
// FormatVerifyKeys via the derive CLI), but cross-package coverage does not count
// toward this package's profile — so they are tested directly here.

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadProvisionedKeyringFromEnv_RoundTrip(t *testing.T) {
	master := mustTestRing(t, testHMACKeyNew, testHMACKeyOld) // current + previous
	pk, err := DeriveProvisionedKeys(master, "configcore", []string{"accesscore"})
	require.NoError(t, err)

	t.Setenv(EnvServiceOwnCell, "configcore")
	t.Setenv(EnvServiceSigningKey, hex.EncodeToString(pk.SigningCurrent))
	t.Setenv(EnvServiceSigningKeyPrevious, hex.EncodeToString(pk.SigningPrevious))
	t.Setenv(EnvServiceVerifyKeys, FormatVerifyKeys(pk.VerifyCurrent))
	t.Setenv(EnvServiceVerifyKeysPrevious, FormatVerifyKeys(pk.VerifyPrevious))

	ring, err := LoadProvisionedKeyringFromEnv()
	require.NoError(t, err)
	require.NoError(t, ring.Validate())

	sig, err := ring.SigningSecrets("configcore")
	require.NoError(t, err)
	assert.Len(t, sig, 2, "current + previous signing subkeys")

	ver, err := ring.VerifySecrets("accesscore")
	require.NoError(t, err)
	assert.Len(t, ver, 2, "current + previous verify subkeys")

	// A token signed by accesscore (master-derived) verifies under this loaded ring.
	tok := signAs(master, "accesscore")
	assert.True(t, verifyTokenWith(t, ring, tok))
}

func TestLoadProvisionedKeyringFromEnv_Errors(t *testing.T) {
	master := mustTestRing(t, testHMACKey, "")
	pk, err := DeriveProvisionedKeys(master, "configcore", []string{"accesscore"})
	require.NoError(t, err)
	goodSig := hex.EncodeToString(pk.SigningCurrent)
	goodVerify := FormatVerifyKeys(pk.VerifyCurrent)

	cases := []struct {
		name                                              string
		ownCell, signing, signingPrev, verify, verifyPrev string
	}{
		{"bad signing hex", "configcore", "NOTHEX", "", goodVerify, ""},
		{"empty signing", "configcore", "", "", goodVerify, ""},
		{"bad signing previous hex", "configcore", goodSig, "ZZZZ", goodVerify, ""},
		{"malformed verify entry", "configcore", goodSig, "", "accesscore-no-colon", ""},
		{"bad verify hex", "configcore", goodSig, "", "accesscore:NOTHEX", ""},
		{"bad verify previous", "configcore", goodSig, "", goodVerify, "accesscore:NOTHEX"},
		{"invalid ownCell", "Bad-Cell", goodSig, "", goodVerify, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvServiceOwnCell, tc.ownCell)
			t.Setenv(EnvServiceSigningKey, tc.signing)
			t.Setenv(EnvServiceSigningKeyPrevious, tc.signingPrev)
			t.Setenv(EnvServiceVerifyKeys, tc.verify)
			t.Setenv(EnvServiceVerifyKeysPrevious, tc.verifyPrev)
			_, err := LoadProvisionedKeyringFromEnv()
			require.Error(t, err)
		})
	}
}

func TestFormatVerifyKeys_RoundTripAndSorted(t *testing.T) {
	assert.Equal(t, "", FormatVerifyKeys(map[string][]byte{}), "empty map → empty string")

	m := map[string][]byte{
		"configcore": []byte("0123456789abcdef0123456789abcdef"),
		"accesscore": []byte("fedcba9876543210fedcba9876543210"),
	}
	s := FormatVerifyKeys(m)
	// callers sorted: accesscore before configcore.
	assert.Regexp(t, `^accesscore:[0-9a-f]+,configcore:[0-9a-f]+$`, s)

	parsed, err := parseVerifyKeys(s)
	require.NoError(t, err)
	assert.Equal(t, m, parsed, "Format → parse round-trips")
}

func TestDeriveProvisionedKeys_Errors(t *testing.T) {
	_, err := DeriveProvisionedKeys(nil, "accesscore", nil)
	require.Error(t, err, "nil master rejected")

	master := mustTestRing(t, testHMACKey, "")
	_, err = DeriveProvisionedKeys(master, "Bad-Cell", nil)
	require.Error(t, err, "invalid ownCell rejected")

	_, err = DeriveProvisionedKeys(master, "accesscore", []string{"Bad-Cell"})
	require.Error(t, err, "invalid caller rejected")
}

func TestNewProvisionedKeyring_EmptySubkeySetsRejected(t *testing.T) {
	good := []byte("0123456789abcdef0123456789abcdef")
	_, err := NewProvisionedKeyring("accesscore", nil, nil)
	require.Error(t, err, "empty signing set rejected")
	_, err = NewProvisionedKeyring("accesscore", [][]byte{}, nil)
	require.Error(t, err, "zero-length signing set rejected")
	_, err = NewProvisionedKeyring("accesscore", [][]byte{good},
		map[string][][]byte{"auditcore": {}})
	require.Error(t, err, "empty verify subkey set rejected")
}

func TestHMACKeyRing_DeriveAll_EmptyCellRejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	_, err := ring.SigningSecrets("")
	require.Error(t, err, "empty ownCell rejected")
	_, err = ring.VerifySecrets("")
	require.Error(t, err, "empty callerCell rejected")
}

func TestHMACKeyRing_Validate_ShortSecretsRejected(t *testing.T) {
	require.NoError(t, mustTestRing(t, testHMACKey, "").Validate())

	// Defensive Validate branches (NewHMACKeyRing rejects short at construction, so
	// build the struct directly to exercise Validate's own guards).
	short := &HMACKeyRing{current: []byte("short")}
	require.Error(t, short.Validate(), "short current rejected")

	shortPrev := &HMACKeyRing{current: []byte(testHMACKey), previous: []byte("short")}
	require.Error(t, shortPrev.Validate(), "short previous rejected")
}
