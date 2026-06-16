package softca_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/softca"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
)

func TestNewDevCA_BuildsTwoTierHierarchy(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ca, err := softca.NewDevCA(clk)
	require.NoError(t, err)

	signer, err := softca.NewSigner(clk, ca, softca.NewMemLedger())
	require.NoError(t, err)
	bundle, err := signer.TrustBundle(context.Background())
	require.NoError(t, err)
	intermediate, root := parseTrustBundle(t, bundle)

	require.True(t, root.IsCA)
	require.True(t, intermediate.IsCA)
	require.NoError(t, intermediate.CheckSignatureFrom(root), "intermediate must be signed by root")
	require.True(t, intermediate.NotAfter.Before(root.NotAfter), "intermediate should be shorter-lived than root")
}

func TestNewFileCA_PersistsAndReloads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// First call bootstraps + persists.
	ca1, err := softca.NewFileCA(clk, dir)
	require.NoError(t, err)
	root1 := trustRoot(t, clk, ca1)

	// Files exist on disk.
	for _, name := range []string{"root.key", "root.crt", "inter.key", "inter.crt"} {
		_, statErr := os.Stat(filepath.Join(dir, name))
		require.NoError(t, statErr, "expected %s on disk", name)
	}

	// Second call reloads the SAME root (no regeneration).
	ca2, err := softca.NewFileCA(clk, dir)
	require.NoError(t, err)
	root2 := trustRoot(t, clk, ca2)
	require.Equal(t, root1, root2, "reloaded CA must present the same root certificate")
}

func TestNewFileCA_CorruptMaterialFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Bootstrap a valid CA, then corrupt one cert file on disk.
	_, err := softca.NewFileCA(clk, dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "inter.crt"), []byte("garbage-not-pem"), 0o644))

	_, err = softca.NewFileCA(clk, dir)
	require.Error(t, err, "corrupt CA material must fail closed on reload")
}

func TestNewFileCA_PartialMaterialFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Only one of the four files present → partial → fail closed (no silent overwrite/load).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "root.key"), []byte("not-a-key"), 0o600))
	_, err := softca.NewFileCA(clk, dir)
	require.Error(t, err, "partial CA material must fail closed")
}

func TestNewCA_NilClockPanics(t *testing.T) {
	t.Parallel()
	require.Panics(t, func() { _, _ = softca.NewDevCA(nil) }, "NewDevCA nil clock must panic")
	require.Panics(t, func() { _, _ = softca.NewFileCA(nil, t.TempDir()) }, "NewFileCA nil clock must panic")
}

// TestNewFileCA_RejectsBroadKeyPerms asserts a key file loosened beyond 0600 on
// reload fails closed — a world/group-readable CA signing key must not be trusted.
func TestNewFileCA_RejectsBroadKeyPerms(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	_, err := softca.NewFileCA(clk, dir) // bootstrap + persist 0600 keys
	require.NoError(t, err)
	require.NoError(t, os.Chmod(filepath.Join(dir, "root.key"), 0o644), "loosen key perms")

	_, err = softca.NewFileCA(clk, dir)
	require.Error(t, err, "a key file with permissions broader than 0600 must fail closed")
}

// TestNewFileCA_RejectsSymlinkKey asserts a key path redirected via symlink fails
// closed (Lstat rejects rather than follows) — custody must not trust a redirect.
func TestNewFileCA_RejectsSymlinkKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	_, err := softca.NewFileCA(clk, dir) // bootstrap + persist valid keys
	require.NoError(t, err)
	interKey := filepath.Join(dir, "inter.key")
	// Replace inter.key with a symlink pointing at the (otherwise valid) root.key:
	// only the symlink check should fire, not a parse/perm failure of the target.
	require.NoError(t, os.Remove(interKey))
	require.NoError(t, os.Symlink(filepath.Join(dir, "root.key"), interKey))

	_, err = softca.NewFileCA(clk, dir)
	require.Error(t, err, "a symlinked key file must fail closed")
}

// TestNewFileCA_RejectsKeyCertMismatch asserts a key file that does not correspond
// to its certificate fails closed — softca must not sign with a key whose public
// half differs from the certificate it presents (every issued leaf would be
// unverifiable). Swapping inter.key for root.key's content reproduces this.
func TestNewFileCA_RejectsKeyCertMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	_, err := softca.NewFileCA(clk, dir) // bootstrap + persist valid material
	require.NoError(t, err)
	rootKeyPEM, err := os.ReadFile(filepath.Join(dir, "root.key")) //nolint:gosec // test fixture path under t.TempDir()
	require.NoError(t, err)
	// inter.key now holds the ROOT private key — a valid PKCS#8 key, 0600, that
	// parses and is a CA, but whose public half ≠ inter.crt's public key.
	//nolint:gosec // test fixture path under t.TempDir(), not attacker-controlled
	require.NoError(t, os.WriteFile(filepath.Join(dir, "inter.key"), rootKeyPEM, 0o600))

	_, err = softca.NewFileCA(clk, dir)
	require.Error(t, err, "a key not matching its certificate must fail closed")
}

// trustRoot returns the DER of the CA's root certificate via the public trust bundle.
func trustRoot(t *testing.T, clk *clockmock.FakeClock, ca *softca.CA) []byte {
	t.Helper()
	signer, err := softca.NewSigner(clk, ca, softca.NewMemLedger())
	require.NoError(t, err)
	bundle, err := signer.TrustBundle(context.Background())
	require.NoError(t, err)
	require.Len(t, bundle, 2)
	return bundle[1]
}
