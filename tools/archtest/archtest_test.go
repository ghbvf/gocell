package archtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readModulePath parses go.mod to extract the module path (e.g. "github.com/ghbvf/gocell").
// This avoids hardcoding the module path, which would silently disable all rules on rename or /v2 bump.
func readModulePath(t *testing.T, modRoot string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(modRoot, "go.mod"))
	require.NoError(t, err, "cannot open go.mod")
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	require.NoError(t, scanner.Err())
	t.Fatal("go.mod has no module directive")
	return ""
}

// pkgInfo holds the subset of `go list -json` output needed for layering checks.
type pkgInfo struct {
	ImportPath string   `json:"ImportPath"`
	Imports    []string `json:"Imports"`
}

// violation describes a single layering rule breach.
type violation struct {
	Rule    string // e.g. "LAYER-01"
	Pkg     string // the offending package
	Import  string // the forbidden import
	Message string
}

// --- helpers (pure functions) ---

// layerOf extracts the top-level directory for an internal module path.
// Returns "" for external packages or the module root itself.
// modPrefix must include trailing slash (e.g. "github.com/ghbvf/gocell/").
func layerOf(modPrefix, importPath string) string {
	if !strings.HasPrefix(importPath, modPrefix) {
		return ""
	}
	rel := strings.TrimPrefix(importPath, modPrefix)
	if rel == "" {
		return "" // module root package, no layer
	}
	parts := strings.SplitN(rel, "/", 2)
	return parts[0]
}

// cellOf extracts the cell ID (e.g. "access-core") from a cells/ package path.
// Returns "" if not under cells/.
func cellOf(modPrefix, importPath string) string {
	cellsPrefix := modPrefix + "cells/"
	if !strings.HasPrefix(importPath, cellsPrefix) {
		return ""
	}
	rel := strings.TrimPrefix(importPath, cellsPrefix)
	parts := strings.SplitN(rel, "/", 2)
	return parts[0]
}

// isInternal returns true if the import path contains an internal package segment.
func isInternal(importPath string) bool {
	return strings.Contains(importPath, "/internal/") || strings.HasSuffix(importPath, "/internal")
}

// checkLayering runs all 5 layering rules against the given packages and returns violations.
// modPrefix must include trailing slash (e.g. "github.com/ghbvf/gocell/").
func checkLayering(modPrefix string, pkgs []pkgInfo) []violation {
	var out []violation

	for _, pkg := range pkgs {
		srcLayer := layerOf(modPrefix, pkg.ImportPath)
		srcCell := cellOf(modPrefix, pkg.ImportPath)

		for _, imp := range pkg.Imports {
			impLayer := layerOf(modPrefix, imp)
			if impLayer == "" {
				continue // external package, skip
			}

			var rule string
			switch {
			// LAYER-01: kernel/ may only import kernel/ and pkg/ (allow-list).
			// Any other internal module import is forbidden.
			case srcLayer == "kernel" && impLayer != "kernel" && impLayer != "pkg":
				rule = "LAYER-01"

			// LAYER-02: cells/ must not import adapters/
			case srcLayer == "cells" && impLayer == "adapters":
				rule = "LAYER-02"

			// LAYER-03: runtime/ must not import cells/ or adapters/
			case srcLayer == "runtime" && (impLayer == "cells" || impLayer == "adapters"):
				rule = "LAYER-03"

			// LAYER-04: adapters/ must not import cells/, cmd/, examples/
			case srcLayer == "adapters" && (impLayer == "cells" || impLayer == "cmd" || impLayer == "examples"):
				rule = "LAYER-04"
			}

			if rule != "" {
				out = append(out, violation{
					Rule:    rule,
					Pkg:     pkg.ImportPath,
					Import:  imp,
					Message: fmt.Sprintf("%s: %s imports %s", rule, pkg.ImportPath, imp),
				})
				continue
			}

			// LAYER-05: no cross-cell internal imports.
			// TODO: L0 Cell exception — CLAUDE.md allows L0 cells to be directly imported
			// by sibling cells in the same assembly. When L0 cells exist under cells/,
			// parse cell.yaml to identify them and skip LAYER-05 for L0 targets.
			if srcCell != "" && isInternal(imp) {
				impCell := cellOf(modPrefix, imp)
				if impCell != "" && impCell != srcCell {
					out = append(out, violation{
						Rule:    "LAYER-05",
						Pkg:     pkg.ImportPath,
						Import:  imp,
						Message: fmt.Sprintf("LAYER-05: %s imports %s (cross-cell internal)", pkg.ImportPath, imp),
					})
				}
			}
		}
	}
	return out
}

// --- go list integration ---

// findModuleRoot walks up from cwd to find the directory containing go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "go.mod not found")
		dir = parent
	}
}

// loadPackages runs `go list -json -e ./...` and parses the concatenated JSON output.
// The -e flag tolerates packages with errors (e.g. Go's internal/ visibility rejection),
// so LAYER-05 violations can be surfaced as rule-specific failures instead of a generic
// command failure that masks other violations.
func loadPackages(t *testing.T, root string) []pkgInfo {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "-e", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "go list -json -e ./... failed: %s", stderr.String())

	var pkgs []pkgInfo
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p pkgInfo
		require.NoError(t, dec.Decode(&p))
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// --- integration test (real go list data) ---

func TestLayeringRules(t *testing.T) {
	root := findModuleRoot(t)
	modPrefix := readModulePath(t, root) + "/"
	pkgs := loadPackages(t, root)
	require.NotEmpty(t, pkgs, "go list returned no packages")

	violations := checkLayering(modPrefix, pkgs)

	// Group violations by rule for readable output.
	byRule := map[string][]string{}
	for _, v := range violations {
		byRule[v.Rule] = append(byRule[v.Rule], v.Message)
	}

	// Summary log for quick diagnosis when multiple rules are violated.
	if len(violations) > 0 {
		t.Logf("Found %d layering violation(s):", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v.Message)
		}
	}

	t.Run("LAYER-01_kernel_no_upward_imports", func(t *testing.T) {
		assert.Empty(t, byRule["LAYER-01"], "kernel/ must not import runtime/, adapters/, or cells/")
	})
	t.Run("LAYER-02_cells_no_adapter_imports", func(t *testing.T) {
		assert.Empty(t, byRule["LAYER-02"], "cells/ must not import adapters/")
	})
	t.Run("LAYER-03_runtime_no_upward_imports", func(t *testing.T) {
		assert.Empty(t, byRule["LAYER-03"], "runtime/ must not import cells/ or adapters/")
	})
	t.Run("LAYER-04_adapters_no_cell_cmd_example_imports", func(t *testing.T) {
		assert.Empty(t, byRule["LAYER-04"], "adapters/ must not import cells/, cmd/, or examples/")
	})
	t.Run("LAYER-05_no_cross_cell_internal_imports", func(t *testing.T) {
		assert.Empty(t, byRule["LAYER-05"], "cells must not import another cell's internal/ packages")
	})
}

// --- unit tests for helper functions ---

func TestLayerOf(t *testing.T) {
	const mod = "github.com/ghbvf/gocell/"
	tests := []struct {
		input string
		want  string
	}{
		{"github.com/ghbvf/gocell/kernel/cell", "kernel"},
		{"github.com/ghbvf/gocell/kernel/outbox", "kernel"},
		{"github.com/ghbvf/gocell/runtime/auth", "runtime"},
		{"github.com/ghbvf/gocell/runtime/http/middleware", "runtime"},
		{"github.com/ghbvf/gocell/adapters/postgres", "adapters"},
		{"github.com/ghbvf/gocell/cells/access-core", "cells"},
		{"github.com/ghbvf/gocell/cells/access-core/internal/domain", "cells"},
		{"github.com/ghbvf/gocell/pkg/errcode", "pkg"},
		{"github.com/ghbvf/gocell/cmd/gocell", "cmd"},
		{"github.com/ghbvf/gocell/examples/sso-bff", "examples"},
		{"github.com/ghbvf/gocell/tools/archtest", "tools"},
		// Module root package returns "" (no layer segment after prefix).
		{"github.com/ghbvf/gocell", ""},
		// External packages return "".
		{"fmt", ""},
		{"github.com/stretchr/testify/assert", ""},
		{"golang.org/x/crypto/bcrypt", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, layerOf(mod, tt.input))
		})
	}
}

func TestCellOf(t *testing.T) {
	const mod = "github.com/ghbvf/gocell/"
	tests := []struct {
		input string
		want  string
	}{
		{"github.com/ghbvf/gocell/cells/access-core", "access-core"},
		{"github.com/ghbvf/gocell/cells/access-core/internal/domain", "access-core"},
		{"github.com/ghbvf/gocell/cells/audit-core/slices/auditappend", "audit-core"},
		{"github.com/ghbvf/gocell/cells/config-core", "config-core"},
		// Non-cell paths return "".
		{"github.com/ghbvf/gocell/kernel/cell", ""},
		{"github.com/ghbvf/gocell/runtime/auth", ""},
		{"fmt", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, cellOf(mod, tt.input))
		})
	}
}

func TestIsInternal(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"github.com/ghbvf/gocell/cells/access-core/internal/domain", true},
		{"github.com/ghbvf/gocell/cells/audit-core/internal", true},
		{"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogin", false},
		{"github.com/ghbvf/gocell/kernel/cell", false},
		{"github.com/ghbvf/gocell/runtime/auth", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, isInternal(tt.input))
		})
	}
}

// --- unit tests for checkLayering (table-driven with mock data) ---

func TestCheckLayering(t *testing.T) {
	const mod = "github.com/ghbvf/gocell/"
	tests := []struct {
		name      string
		pkgs      []pkgInfo
		wantRules []string // expected rule codes in violations
	}{
		{
			name: "LAYER-01 violation: kernel imports runtime",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/kernel/cell", Imports: []string{
					"fmt",
					"github.com/ghbvf/gocell/pkg/errcode",
					"github.com/ghbvf/gocell/runtime/auth", // forbidden
				}},
			},
			wantRules: []string{"LAYER-01"},
		},
		{
			name: "LAYER-01 violation: kernel imports adapters",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/kernel/outbox", Imports: []string{
					"github.com/ghbvf/gocell/adapters/postgres", // forbidden
				}},
			},
			wantRules: []string{"LAYER-01"},
		},
		{
			name: "LAYER-01 violation: kernel imports cells",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/kernel/assembly", Imports: []string{
					"github.com/ghbvf/gocell/cells/access-core", // forbidden
				}},
			},
			wantRules: []string{"LAYER-01"},
		},
		{
			name: "LAYER-01 violation: kernel imports cmd (allow-list catch-all)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/kernel/cell", Imports: []string{
					"github.com/ghbvf/gocell/cmd/gocell", // forbidden by allow-list
				}},
			},
			wantRules: []string{"LAYER-01"},
		},
		{
			name: "LAYER-01 clean: kernel imports kernel (allowed)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/kernel/governance", Imports: []string{
					"github.com/ghbvf/gocell/kernel/metadata",
					"github.com/ghbvf/gocell/kernel/registry",
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-01 clean: kernel imports pkg (allowed)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/kernel/cell", Imports: []string{
					"fmt",
					"github.com/ghbvf/gocell/pkg/errcode",
					"github.com/ghbvf/gocell/pkg/ctxkeys",
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-02 violation: cells imports adapters",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/access-core", Imports: []string{
					"github.com/ghbvf/gocell/kernel/cell",
					"github.com/ghbvf/gocell/adapters/postgres", // forbidden
				}},
			},
			wantRules: []string{"LAYER-02"},
		},
		{
			name: "LAYER-02 clean: cells imports kernel + runtime (allowed)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/access-core", Imports: []string{
					"github.com/ghbvf/gocell/kernel/cell",
					"github.com/ghbvf/gocell/runtime/auth",
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-03 violation: runtime imports cells",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/runtime/eventbus", Imports: []string{
					"github.com/ghbvf/gocell/cells/audit-core", // forbidden
				}},
			},
			wantRules: []string{"LAYER-03"},
		},
		{
			name: "LAYER-03 violation: runtime imports adapters",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/runtime/config", Imports: []string{
					"github.com/ghbvf/gocell/adapters/redis", // forbidden
				}},
			},
			wantRules: []string{"LAYER-03"},
		},
		{
			name: "LAYER-03 clean: runtime imports kernel + pkg (allowed)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/runtime/eventbus", Imports: []string{
					"github.com/ghbvf/gocell/kernel/outbox",
					"github.com/ghbvf/gocell/pkg/errcode",
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-04 violation: adapters imports cells",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/adapters/redis", Imports: []string{
					"github.com/ghbvf/gocell/cells/config-core", // forbidden
				}},
			},
			wantRules: []string{"LAYER-04"},
		},
		{
			name: "LAYER-04 violation: adapters imports cmd",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/adapters/postgres", Imports: []string{
					"github.com/ghbvf/gocell/cmd/gocell", // forbidden
				}},
			},
			wantRules: []string{"LAYER-04"},
		},
		{
			name: "LAYER-04 violation: adapters imports examples",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/adapters/redis", Imports: []string{
					"github.com/ghbvf/gocell/examples/sso-bff", // forbidden
				}},
			},
			wantRules: []string{"LAYER-04"},
		},
		{
			name: "LAYER-04 clean: adapters imports kernel + runtime (allowed)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/adapters/postgres", Imports: []string{
					"github.com/ghbvf/gocell/kernel/persistence",
					"github.com/ghbvf/gocell/runtime/observability/logging",
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-05 violation: cross-cell internal import",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/audit-core/slices/auditappend", Imports: []string{
					"github.com/ghbvf/gocell/cells/access-core/internal/domain", // forbidden
				}},
			},
			wantRules: []string{"LAYER-05"},
		},
		{
			name: "LAYER-05 clean: same-cell internal import (allowed)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/audit-core/slices/auditappend", Imports: []string{
					"github.com/ghbvf/gocell/cells/audit-core/internal/domain", // same cell, OK
				}},
			},
			wantRules: nil,
		},
		{
			name: "multiple violations across rules",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/kernel/cell", Imports: []string{
					"github.com/ghbvf/gocell/runtime/auth",
				}},
				{ImportPath: "github.com/ghbvf/gocell/cells/access-core", Imports: []string{
					"github.com/ghbvf/gocell/adapters/postgres",
				}},
				{ImportPath: "github.com/ghbvf/gocell/runtime/worker", Imports: []string{
					"github.com/ghbvf/gocell/adapters/redis",
				}},
			},
			wantRules: []string{"LAYER-01", "LAYER-02", "LAYER-03"},
		},
		{
			name: "clean: cmd imports all layers (no rule restricts cmd)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cmd/gocell", Imports: []string{
					"github.com/ghbvf/gocell/kernel/cell",
					"github.com/ghbvf/gocell/runtime/auth",
					"github.com/ghbvf/gocell/adapters/postgres",
					"github.com/ghbvf/gocell/cells/access-core",
				}},
			},
			wantRules: nil,
		},
		{
			name: "clean: examples imports all layers (unrestricted)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/examples/sso-bff", Imports: []string{
					"github.com/ghbvf/gocell/kernel/cell",
					"github.com/ghbvf/gocell/runtime/auth",
					"github.com/ghbvf/gocell/adapters/postgres",
					"github.com/ghbvf/gocell/cells/access-core",
				}},
			},
			wantRules: nil,
		},
		{
			name: "clean: pkg imports nothing forbidden (no rule restricts pkg)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/pkg/errcode", Imports: []string{
					"fmt", "net/http",
				}},
			},
			wantRules: nil,
		},
		{
			name:      "empty package list",
			pkgs:      nil,
			wantRules: nil,
		},
		{
			name: "only external imports (no violations)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/kernel/cell", Imports: []string{
					"fmt", "context", "github.com/google/uuid",
				}},
			},
			wantRules: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations := checkLayering(mod, tt.pkgs)

			gotRules := make([]string, 0, len(violations))
			seen := map[string]bool{}
			for _, v := range violations {
				if !seen[v.Rule] {
					gotRules = append(gotRules, v.Rule)
					seen[v.Rule] = true
				}
			}

			if tt.wantRules == nil {
				assert.Empty(t, violations, "expected no violations")
			} else {
				assert.Equal(t, tt.wantRules, gotRules, "violation rules mismatch")
				// Verify each violation has all fields populated.
				for _, v := range violations {
					assert.NotEmpty(t, v.Rule, "violation.Rule must not be empty")
					assert.NotEmpty(t, v.Pkg, "violation.Pkg must not be empty")
					assert.NotEmpty(t, v.Import, "violation.Import must not be empty")
					assert.NotEmpty(t, v.Message, "violation.Message must not be empty")
				}
			}
		})
	}
}
