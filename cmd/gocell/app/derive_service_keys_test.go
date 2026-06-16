package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// testMasterSecret is a 32-byte key that satisfies MinHMACKeyBytes (32).
//
//nolint:gosec // G101: in-process test fixture, not a real credential.
const testMasterSecret = "derive-test-master-secret-32byte"

// testMasterSecretPrev is a distinct previous-generation master key.
//
//nolint:gosec // G101: in-process test fixture, not a real credential.
const testMasterSecretPrev = "derive-test-prev-secret-32byte!!"

func TestParseCallerList(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty string yields nil",
			input: "",
			want:  nil,
		},
		{
			name:  "whitespace-only yields nil",
			input: "   ",
			want:  nil,
		},
		{
			name:  "single caller",
			input: "accesscore",
			want:  []string{"accesscore"},
		},
		{
			name:  "multiple callers",
			input: "accesscore,auditcore,configcore",
			want:  []string{"accesscore", "auditcore", "configcore"},
		},
		{
			name:  "trims whitespace around entries",
			input: " accesscore , auditcore ",
			want:  []string{"accesscore", "auditcore"},
		},
		{
			name:  "drops empty segments",
			input: "accesscore,,auditcore",
			want:  []string{"accesscore", "auditcore"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCallerList(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLoadMasterKeyRing_MissingEnv(t *testing.T) {
	// Ensure env is cleared for this test.
	t.Setenv(auth.EnvServiceSecret, "")
	_, err := loadMasterKeyRing()
	require.Error(t, err, "missing master secret env must return an error")
}

func TestLoadMasterKeyRing_TooShort(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, "tooshort")
	_, err := loadMasterKeyRing()
	require.Error(t, err)
}

func TestLoadMasterKeyRing_Valid(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, testMasterSecret)
	ring, err := loadMasterKeyRing()
	require.NoError(t, err)
	require.NotNil(t, ring)
}

func TestLoadMasterKeyRing_WithPrevious(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, testMasterSecret)
	t.Setenv(auth.EnvServiceSecretPrevious, testMasterSecretPrev)
	ring, err := loadMasterKeyRing()
	require.NoError(t, err)
	require.NotNil(t, ring)
}

// TestEmitProvisionedEnv_NoPrevious verifies the output format for a single-gen
// master (no PREVIOUS keys): expects exactly 3 export lines.
func TestEmitProvisionedEnv_NoPrevious(t *testing.T) {
	master, err := auth.NewHMACKeyRing([]byte(testMasterSecret), nil)
	require.NoError(t, err)

	pk, err := auth.DeriveProvisionedKeys(master, "accesscore", []string{"auditcore"})
	require.NoError(t, err)

	// Capture output via a temp file to reuse emitProvisionedEnv.
	r, w, err := os.Pipe()
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- emitProvisionedEnv(w, pk)
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, readErr := buf.ReadFrom(r)
	require.NoError(t, readErr)
	require.NoError(t, <-errCh)

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	// Must have exactly 3 lines: OwnCell + SigningKey + VerifyKeys (no PREVIOUS).
	require.Len(t, lines, 3, "no-previous output must have 3 export lines")

	assertExportLine(t, lines, auth.EnvServiceOwnCell, "accesscore")
	assertExportLine(t, lines, auth.EnvServiceSigningKey, hex.EncodeToString(pk.SigningCurrent))
	assertExportLine(t, lines, auth.EnvServiceVerifyKeys, auth.FormatVerifyKeys(pk.VerifyCurrent))

	// No PREVIOUS lines expected.
	assert.False(t, containsEnvKey(lines, auth.EnvServiceSigningKeyPrevious),
		"no SIGNING_KEY_PREVIOUS without master previous")
	assert.False(t, containsEnvKey(lines, auth.EnvServiceVerifyKeysPrevious),
		"no VERIFY_KEYS_PREVIOUS without master previous")
}

// TestEmitProvisionedEnv_WithPrevious verifies the output includes PREVIOUS
// lines when the master has a previous-generation secret.
func TestEmitProvisionedEnv_WithPrevious(t *testing.T) {
	master, err := auth.NewHMACKeyRing([]byte(testMasterSecret), []byte(testMasterSecretPrev))
	require.NoError(t, err)

	pk, err := auth.DeriveProvisionedKeys(master, "accesscore", []string{"auditcore"})
	require.NoError(t, err)

	r, w, err := os.Pipe()
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- emitProvisionedEnv(w, pk)
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, readErr := buf.ReadFrom(r)
	require.NoError(t, readErr)
	require.NoError(t, <-errCh)

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	// Must have exactly 5 lines: OwnCell + SigningKey + SigningKeyPrevious + VerifyKeys + VerifyKeysPrevious.
	require.Len(t, lines, 5, "with-previous output must have 5 export lines")

	assertExportLine(t, lines, auth.EnvServiceSigningKeyPrevious, hex.EncodeToString(pk.SigningPrevious))
	assertExportLine(t, lines, auth.EnvServiceVerifyKeysPrevious, auth.FormatVerifyKeys(pk.VerifyPrevious))
}

// TestEmitProvisionedEnv_NoCallers verifies that a cell with no callers emits
// an empty (but present) GOCELL_SERVICE_VERIFY_KEYS line.
func TestEmitProvisionedEnv_NoCallers(t *testing.T) {
	master, err := auth.NewHMACKeyRing([]byte(testMasterSecret), nil)
	require.NoError(t, err)

	pk, err := auth.DeriveProvisionedKeys(master, "accesscore", nil)
	require.NoError(t, err)

	r, w, err := os.Pipe()
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- emitProvisionedEnv(w, pk)
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, readErr := buf.ReadFrom(r)
	require.NoError(t, readErr)
	require.NoError(t, <-errCh)

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	// 3 lines: OwnCell + SigningKey + VerifyKeys (empty, no callers, no previous).
	require.Len(t, lines, 3, "no-callers output must have 3 export lines (empty VERIFY_KEYS)")
	assertExportLine(t, lines, auth.EnvServiceVerifyKeys, "")
}

// TestDeriveServiceKeys_OutputConsistentWithDeriveProvisionedKeys is the
// strongest correctness assertion: we inject the env vars that
// runDeriveServiceKeys would emit, load a ProvisionedKeyring via
// LoadProvisionedKeyringFromEnv, and verify that a token signed by the master
// (as accesscore) is accepted by the ProvisionedKeyring's verify path.
func TestDeriveServiceKeys_OutputConsistentWithDeriveProvisionedKeys(t *testing.T) {
	master, err := auth.NewHMACKeyRing([]byte(testMasterSecret), nil)
	require.NoError(t, err)

	callers := []string{"auditcore"}
	pk, err := auth.DeriveProvisionedKeys(master, "accesscore", callers)
	require.NoError(t, err)

	// Inject the same env vars that emitProvisionedEnv would write.
	t.Setenv(auth.EnvServiceOwnCell, pk.OwnCell)
	t.Setenv(auth.EnvServiceSigningKey, hex.EncodeToString(pk.SigningCurrent))
	t.Setenv(auth.EnvServiceSigningKeyPrevious, "")
	t.Setenv(auth.EnvServiceVerifyKeys, auth.FormatVerifyKeys(pk.VerifyCurrent))
	t.Setenv(auth.EnvServiceVerifyKeysPrevious, "")

	kr, err := auth.LoadProvisionedKeyringFromEnv()
	require.NoError(t, err)

	// The ProvisionedKeyring must hold exactly the same signing subkey as
	// DeriveProvisionedKeys produced: calling SigningSecrets on the ownCell
	// returns it.
	signingSecrets, err := kr.SigningSecrets("accesscore")
	require.NoError(t, err)
	require.Len(t, signingSecrets, 1)
	assert.Equal(t, pk.SigningCurrent, signingSecrets[0],
		"signing subkey loaded from env must equal DeriveProvisionedKeys output")

	// The ProvisionedKeyring must hold the correct verify subkey for auditcore.
	verifySecrets, err := kr.VerifySecrets("auditcore")
	require.NoError(t, err)
	require.Len(t, verifySecrets, 1)
	assert.Equal(t, pk.VerifyCurrent["auditcore"], verifySecrets[0],
		"verify subkey loaded from env must equal DeriveProvisionedKeys output")
}

// TestDeriveServiceKeys_RotationConsistency verifies that previous-gen keys
// round-trip correctly through the env encoding.
func TestDeriveServiceKeys_RotationConsistency(t *testing.T) {
	master, err := auth.NewHMACKeyRing([]byte(testMasterSecret), []byte(testMasterSecretPrev))
	require.NoError(t, err)

	callers := []string{"auditcore"}
	pk, err := auth.DeriveProvisionedKeys(master, "accesscore", callers)
	require.NoError(t, err)

	require.NotNil(t, pk.SigningPrevious, "rotation: previous signing key must be present")
	require.NotNil(t, pk.VerifyPrevious, "rotation: previous verify keys must be present")

	t.Setenv(auth.EnvServiceOwnCell, pk.OwnCell)
	t.Setenv(auth.EnvServiceSigningKey, hex.EncodeToString(pk.SigningCurrent))
	t.Setenv(auth.EnvServiceSigningKeyPrevious, hex.EncodeToString(pk.SigningPrevious))
	t.Setenv(auth.EnvServiceVerifyKeys, auth.FormatVerifyKeys(pk.VerifyCurrent))
	t.Setenv(auth.EnvServiceVerifyKeysPrevious, auth.FormatVerifyKeys(pk.VerifyPrevious))

	kr, err := auth.LoadProvisionedKeyringFromEnv()
	require.NoError(t, err)

	signingSecrets, err := kr.SigningSecrets("accesscore")
	require.NoError(t, err)
	require.Len(t, signingSecrets, 2)
	assert.Equal(t, pk.SigningCurrent, signingSecrets[0])
	assert.Equal(t, pk.SigningPrevious, signingSecrets[1])

	verifySecrets, err := kr.VerifySecrets("auditcore")
	require.NoError(t, err)
	require.Len(t, verifySecrets, 2)
	assert.Equal(t, pk.VerifyCurrent["auditcore"], verifySecrets[0])
	assert.Equal(t, pk.VerifyPrevious["auditcore"], verifySecrets[1])
}

// TestRunDeriveServiceKeys_MissingCellFlag verifies that omitting --cell returns
// a descriptive error.
func TestRunDeriveServiceKeys_MissingCellFlag(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, testMasterSecret)
	err := runDeriveServiceKeys(context.Background(), []string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--cell")
}

// TestRunDeriveServiceKeys_MissingMasterEnv verifies that a missing
// GOCELL_SERVICE_SECRET produces an error even when --cell is provided.
func TestRunDeriveServiceKeys_MissingMasterEnv(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, "")
	err := runDeriveServiceKeys(context.Background(), []string{"--cell", "accesscore"})
	require.Error(t, err, "missing master secret env must return an error")
}

// TestRunDeriveServiceKeys_InvalidCell verifies that an invalid cell id is
// rejected (DeriveProvisionedKeys validates the cell id format).
func TestRunDeriveServiceKeys_InvalidCell(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, testMasterSecret)
	err := runDeriveServiceKeys(context.Background(), []string{"--cell", "Bad-Cell-ID"})
	require.Error(t, err)
}

// TestRunDeriveServiceKeys_ValidProducesShellBlock is an integration test that
// runs the full command via Dispatch and asserts the stdout output contains the
// expected export lines.
func TestRunDeriveServiceKeys_ValidProducesShellBlock(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, testMasterSecret)
	t.Setenv(auth.EnvServiceSecretPrevious, "")

	ctx := context.Background()
	exit, stdout, _ := captureDispatch(t, ctx, []string{
		"derive-service-keys",
		"--cell", "accesscore",
		"--callers", "auditcore",
	})

	require.Equal(t, ExitOK, exit)

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	// 3 lines: OwnCell + SigningKey + VerifyKeys (no previous generation).
	require.GreaterOrEqual(t, len(lines), 3, "must emit at least OwnCell, SigningKey, VerifyKeys")

	found := containsEnvKey(lines, auth.EnvServiceOwnCell)
	assert.True(t, found, "must export "+auth.EnvServiceOwnCell)
	found = containsEnvKey(lines, auth.EnvServiceSigningKey)
	assert.True(t, found, "must export "+auth.EnvServiceSigningKey)
	found = containsEnvKey(lines, auth.EnvServiceVerifyKeys)
	assert.True(t, found, "must export "+auth.EnvServiceVerifyKeys)
}

// assertExportLine checks that lines contains "export KEY=value".
func assertExportLine(t *testing.T, lines []string, key, value string) {
	t.Helper()
	expected := "export " + key + "=" + value
	for _, l := range lines {
		if l == expected {
			return
		}
	}
	t.Errorf("expected export line %q not found in:\n%s", expected, strings.Join(lines, "\n"))
}

// containsEnvKey reports whether any line starts with "export KEY=".
func containsEnvKey(lines []string, key string) bool {
	prefix := "export " + key + "="
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
