package contracttest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeT captures Fatalf for path-traversal negative tests so the test binary
// doesn't actually fail; we just inspect whether the helper rejected the
// input.
type fakeT struct {
	testing.TB
	failed bool
	msg    string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed = true
	f.msg = strings.TrimSpace(fmt.Sprintf(format, args...))
}

func TestPathWithinAllowList(t *testing.T) {
	contractDir := filepath.Join(testdataRoot(), "http", "test", "valid", "v1")

	cases := []struct {
		name     string
		filename string
		wantOK   bool
		desc     string
	}{
		{"in_dir_simple", "request.schema.json", true, "same-dir schema reference"},
		{
			name: "shared_cross_ref",
			// 4-up navigation matches the canonical real-world pattern
			// (contracts/<kind>/<domain>/<v>/ → ../../../../shared/...).
			filename: "../../../../shared/errors/error-response-v1.schema.json",
			wantOK:   true,
			desc:     "valid cross-contract shared schema under sibling contractsRoot/shared",
		},
		{
			name:     "escape_to_repo_outside_rejected",
			filename: strings.Repeat("../", 30) + "etc/passwd",
			wantOK:   false,
			desc:     "navigating above any contracts/ ancestor fails",
		},
		{
			name:     "inside_repo_but_not_shared_rejected",
			filename: "../../../../../README.md",
			wantOK:   false,
			desc:     "outside contractsRoot/shared subtree fails (escapes contractsRoot)",
		},
		{
			name:     "escape_via_shared_then_up_rejected",
			filename: "../../../../shared/../../README.md",
			wantOK:   false,
			desc:     "shared/ prefix that re-escapes upward via .. fails after Clean",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeT{TB: t}
			ok := pathWithinAllowList(ft, filepath.Clean(contractDir), filepath.Clean(filepath.Join(contractDir, tc.filename)))
			if tc.wantOK {
				if !ok {
					t.Errorf("%s: expected accept, got reject; helper Fatalf=%q", tc.desc, ft.msg)
				}
				if ft.failed {
					t.Errorf("%s: helper called Fatalf for accepted input: %q", tc.desc, ft.msg)
				}
			} else if ok && !ft.failed {
				t.Errorf("%s: expected reject, got accept", tc.desc)
			}
		})
	}
}

// Integration coverage: existing extra_schema_refs_test and error_response_test
// exercise compileSchemaFile end-to-end via Load/ValidateErrorResponse with the
// shared/ cross-ref pattern; pathWithinAllowList unit tests above cover the
// security boundary in isolation. We deliberately skip wrapping fakeT around
// compileSchemaFile because *testing.T.Fatalf aborts via runtime.Goexit (which
// fakeT cannot replicate without spawning its own goroutine), and the negative
// security checks are fully exercised at the unit layer.

// TestPathWithinAllowList_RejectsSymlinkEscape covers two real attack vectors
// missed by purely lexical HasPrefix checks: (a) a symlink under
// contracts/shared/ pointing to a directory outside the allow-list, and
// (b) a symlink to a file outside the allow-list. evalSymlinkOrSelf in the
// helper resolves symlinks (including on the deepest existing ancestor when
// the leaf does not exist) before the prefix comparison so the resolved
// target — not the symlink path — is what gets gated.
func TestPathWithinAllowList_RejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require admin privileges on Windows")
	}
	tmp := t.TempDir()
	// Build a fake contracts tree: <tmp>/contracts/http/foo/v1/ + <tmp>/contracts/shared/
	contractDir := filepath.Join(tmp, "contracts", "http", "foo", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	sharedDir := filepath.Join(tmp, "contracts", "shared")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	cleanContractDir := filepath.Clean(contractDir)

	t.Run("symlink_to_file_outside_repo_rejected", func(t *testing.T) {
		// Plant a real file outside the contracts tree, then a symlink under
		// shared/ pointing at it. EvalSymlinks resolves the symlink → the
		// resolved path is outside sharedRoot → reject.
		outside := t.TempDir()
		secretPath := filepath.Join(outside, "secret.json")
		require.NoError(t, os.WriteFile(secretPath, []byte("x"), 0o644))
		symlinkPath := filepath.Join(sharedDir, "evil-secret.json")
		require.NoError(t, os.Symlink(secretPath, symlinkPath))

		fullPath := filepath.Clean(filepath.Join(cleanContractDir, "..", "..", "..", "shared", "evil-secret.json"))
		ft := &fakeT{TB: t}
		ok := pathWithinAllowList(ft, cleanContractDir, fullPath)
		assert.False(t, ok, "symlink resolving outside contractsRoot/shared must be rejected")
	})

	t.Run("symlink_dir_with_nonexistent_leaf_rejected", func(t *testing.T) {
		// Symlink shared/evil-dir → outside dir (no leaf file created).
		// evalSymlinkOrSelf must walk up to the existing parent (the symlink
		// itself), resolve it, then re-attach the leaf. Lexical HasPrefix
		// would otherwise accept because the unresolved fullPath stays under
		// shared/ syntactically.
		outside := t.TempDir()
		symlinkDir := filepath.Join(sharedDir, "evil-dir")
		require.NoError(t, os.Symlink(outside, symlinkDir))

		fullPath := filepath.Clean(filepath.Join(cleanContractDir, "..", "..", "..", "shared", "evil-dir", "schema.json"))
		ft := &fakeT{TB: t}
		ok := pathWithinAllowList(ft, cleanContractDir, fullPath)
		assert.False(t, ok, "symlink dir with non-existent leaf must still be resolved + rejected")
	})
}
