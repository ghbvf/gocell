package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

func TestCheckContractHealthCI(t *testing.T) {
	// Run against the real project — should pass with 0 issues.
	err := runCheck([]string{"contract-health"})
	assert.NoError(t, err, "contract-health should pass on the project's contracts")
}

// TestCheckContractHealth_JSONFormat verifies --format=json emits a
// machine-readable document. We skip when running outside the gocell tree
// (the real project must be reachable for findRoot()), and we only check
// the structural shape — exact issue list depends on repo state.
func TestCheckContractHealth_JSONFormat(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"contract-health", "--format=json"})
	})
	require.NotEmpty(t, out, "JSON format must produce output")

	var doc struct {
		Issues  []map[string]any `json:"issues"`
		Summary struct {
			Errors   int `json:"errors"`
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc),
		"--format=json output must be parseable JSON: %q", out)
	assert.NotNil(t, doc.Issues, "issues key must be present (never null)")
	// Confirm no spurious table rendering leaked into JSON mode.
	assert.NotContains(t, out, "Contract Health (",
		"text-mode table must not appear in JSON output")
	assert.NotContains(t, out, "PASS: all contracts healthy",
		"text-mode trailing line must not appear in JSON output")
}

// TestCheckContractHealth_TextFormat_HasMethodPathColumns verifies the
// PR239-OB1 enhancement: METHOD and PATH columns appear in the human
// table. Both the header row and at least one HTTP contract row should
// carry the data, so dashboards can read transport metadata directly from
// `gocell check contract-health` output.
func TestCheckContractHealth_TextFormat_HasMethodPathColumns(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"contract-health"})
	})
	assert.Contains(t, out, "METHOD",
		"PR239-OB1: text table must have a METHOD column header")
	assert.Contains(t, out, "PATH",
		"PR239-OB1: text table must have a PATH column header")
}

// TestCheckContractHealth_UnknownFormat verifies the dispatcher errors out
// on unknown format strings rather than silently emitting the default —
// catches typos before they become silent CI passes.
func TestCheckContractHealth_UnknownFormat(t *testing.T) {
	err := runCheck([]string{"contract-health", "--format=yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown format")
}

// TestCheckUnconditionalSkipCI runs the analyzer against the real project.
// The repo is expected to stay clean — any drift fails this test. Pairs
// with the kernel-shard CI step (`gocell check unconditional-skip ./...`)
// so local dev catches regressions before push.
func TestCheckUnconditionalSkipCI(t *testing.T) {
	err := runCheck([]string{"unconditional-skip"})
	assert.NoError(t, err, "unconditional-skip must stay at zero on the project")
}

// TestCheckUnconditionalSkip_JSONFormat verifies --format=json emits a
// machine-readable document and that text-mode summary lines never leak
// into the JSON output.
func TestCheckUnconditionalSkip_JSONFormat(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"unconditional-skip", "--format=json"})
	})
	require.NotEmpty(t, out, "JSON format must produce output")

	var doc struct {
		Issues []map[string]any `json:"issues"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc),
		"--format=json output must be parseable JSON: %q", out)
	assert.NotContains(t, out, "Scanned scope:",
		"text-mode scope hint must not appear in JSON output")
	assert.NotContains(t, out, "PASS: no unconditional skips",
		"text-mode trailing line must not appear in JSON output")
}

// TestCheckUnconditionalSkip_TextFormat_PrintsScope confirms that text-mode
// invocations print the scanned root + patterns. Required so users running
// the CLI from a sub-directory don't misread a sub-tree PASS as a repo-wide
// pass — the printed scope reveals the boundary at a glance.
func TestCheckUnconditionalSkip_TextFormat_PrintsScope(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"unconditional-skip"})
	})
	assert.Contains(t, out, "Scanned scope:",
		"text mode must print the scan scope so users can verify boundary")
	assert.Contains(t, out, "PASS: no unconditional skips found",
		"clean repo must emit the trailing PASS line")
}

// TestCheckUnconditionalSkip_UnknownFormat mirrors the contract-health
// dispatch contract — unknown --format strings must error out instead of
// silently degrading to default output.
func TestCheckUnconditionalSkip_UnknownFormat(t *testing.T) {
	err := runCheck([]string{"unconditional-skip", "--format=yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown format")
}

// TestRunUnconditionalSkipAnalyzer_BoundedToRoot pins the Config.Dir
// contract: the analyzer must scan from the project root regardless of
// the CWD that invoked it. Without Dir=root, packages.Load resolves
// "./..." against the caller's CWD and a sub-tree scan can silently mask
// violations elsewhere in the repo.
func TestRunUnconditionalSkipAnalyzer_BoundedToRoot(t *testing.T) {
	root, err := findRoot()
	require.NoError(t, err)

	// Switch CWD to a sub-directory and confirm the analyzer still scans
	// the whole repo — i.e. the Config.Dir override is in effect.
	wd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(filepath.Join(root, "cmd", "gocell")))

	results, err := runUnconditionalSkipAnalyzer([]string{"./..."}, root)
	require.NoError(t, err)
	// Repo is clean (PR-CFG-D removed all stub skips); a sub-tree scan
	// from cmd/gocell would pass anyway, but a true repo-wide scan also
	// passes — the assertion is "no error", not "specific findings".
	assert.Empty(t, results, "clean repo must produce zero findings even from sub-dir CWD")
}

// TestCollectPackageErrors confirms that per-package load errors are
// surfaced as a structured aggregate, not silently swallowed via stderr.
// PR#270's SARIF contract demands errors land in stdout so JSON/SARIF
// consumers can ingest them.
func TestCollectPackageErrors(t *testing.T) {
	t.Run("nil pkgs returns nil", func(t *testing.T) {
		assert.NoError(t, collectPackageErrors(nil))
	})
	t.Run("packages with errors aggregate", func(t *testing.T) {
		pkgs := []*packages.Package{
			{Errors: []packages.Error{{Msg: "first error"}}},
			{Errors: []packages.Error{{Msg: "second error"}}},
		}
		err := collectPackageErrors(pkgs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "first error")
		assert.Contains(t, err.Error(), "second error")
	})
	t.Run("packages without errors returns nil", func(t *testing.T) {
		pkgs := []*packages.Package{{}, {}}
		assert.NoError(t, collectPackageErrors(pkgs))
	})
}

// TestRelativeToRoot pins the SARIF SRCROOT contract: file paths handed to
// printers must be repo-relative slash-separated, not absolute. Regression
// guard for F-R2-4 (PR#276 round-2): pos.Filename from go/packages is
// absolute on every platform, and feeding it raw to normalizeArtifactURI
// emits `<SRCROOT>/Users/...` which GitHub Code Scanning cannot map back.
func TestRelativeToRoot(t *testing.T) {
	tests := []struct {
		name string
		root string
		abs  string
		want string
	}{
		{
			name: "absolute path under root → relative slash path",
			root: "/repo",
			abs:  "/repo/cmd/gocell/app/check.go",
			want: "cmd/gocell/app/check.go",
		},
		{
			name: "empty filename → empty",
			root: "/repo",
			abs:  "",
			want: "",
		},
		{
			name: "path outside root → still relative (filepath.Rel inserts ..)",
			root: "/repo",
			abs:  "/other/foo.go",
			want: "../other/foo.go",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := relativeToRoot(tc.root, tc.abs)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestHTTPTransportColumns covers the cell-table helper directly — both
// branches: HTTP contract with method+path, and non-HTTP / missing
// transport gets "-" placeholders so the table column widths stay stable.
func TestHTTPTransportColumns(t *testing.T) {
	tests := []struct {
		name       string
		c          *metadata.ContractMeta
		wantMethod string
		wantPath   string
	}{
		{
			name: "http with method+path",
			c: &metadata.ContractMeta{
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Method: "GET",
						Path:   "/api/v1/things",
					},
				},
			},
			wantMethod: "GET",
			wantPath:   "/api/v1/things",
		},
		{
			name: "http with empty method renders dash",
			c: &metadata.ContractMeta{
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{Path: "/x"},
				},
			},
			wantMethod: "-",
			wantPath:   "/x",
		},
		{
			name: "event contract gets dashes",
			c: &metadata.ContractMeta{
				Kind: "event",
			},
			wantMethod: "-",
			wantPath:   "-",
		},
		{
			name:       "http with nil HTTP transport gets dashes",
			c:          &metadata.ContractMeta{Kind: "http"},
			wantMethod: "-",
			wantPath:   "-",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, path := httpTransportColumns(tt.c)
			assert.Equal(t, tt.wantMethod, method)
			assert.Equal(t, tt.wantPath, path)
		})
	}
}
