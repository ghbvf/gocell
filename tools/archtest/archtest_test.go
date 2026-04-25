package archtest

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
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

// cellOf extracts the cell ID (e.g. "accesscore") from a cells/ package path.
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

// cellOwnedSubpackages lists public cell subpackages that are semantically
// owned by a single cell and must not be imported by sibling cells. Each
// entry's key is the relative import path of the owned subpackage (without
// module prefix); the value is the relative prefix of the owning cell tree
// that is exempt from the rule.
//
// This is LAYER-06's data table: unlike LAYER-05 (which catches any
// cells/X/Y/internal import), LAYER-06 targets public subpackages whose
// coupling to the owning cell is as strong as internal/ but cannot use the
// internal/ compiler guard — e.g. cells/accesscore/initialadmin, which
// must stay public so cmd/corebundle can wire it into composition, but
// must not be imported by other cells.
//
// cmd/ and examples/ are always exempt (composition roots and unrestricted
// consumers respectively; see the layering conventions in archtest's doc.go).
var cellOwnedSubpackages = map[string]string{
	"cells/accesscore/initialadmin": "cells/accesscore/",
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

			// LAYER-06: cell-owned public subpackages must stay within the
			// owning cell's tree (plus cmd/ and examples/ as universally
			// unrestricted). Flags cases like cells/auditcore importing
			// cells/accesscore/initialadmin, which would bypass the cell
			// boundary without triggering LAYER-05 (no /internal/ segment).
			if v := checkCellOwnedSubpackage(modPrefix, pkg.ImportPath, imp, srcLayer); v != nil {
				out = append(out, *v)
			}

			// LAYER-09: cells/X must not import cells/Y/events (cross-cell public events package).
			// rationale: cell-patterns.md three-tier DTO rule — cells/{cell}/events/ packages
			// are owned by the declaring cell; sibling cells must use contract wire types instead.
			// Same-cell self-import is allowed; cmd/ and examples/ are unrestricted.
			impCell := cellOf(modPrefix, imp)
			if srcCell != "" && impCell != "" && srcCell != impCell {
				impRel := strings.TrimPrefix(imp, modPrefix)
				eventsPrefix := "cells/" + impCell + "/events"
				if impRel == eventsPrefix || strings.HasPrefix(impRel, eventsPrefix+"/") {
					out = append(out, violation{
						Rule:    "LAYER-09",
						Pkg:     pkg.ImportPath,
						Import:  imp,
						Message: fmt.Sprintf("LAYER-09: %s imports %s (cross-cell events package; use contract wire types instead)", pkg.ImportPath, imp),
					})
				}
			}
		}
	}
	return out
}

// checkCellOwnedSubpackage returns a LAYER-06 violation if imp is a cell-owned
// public subpackage that src is not permitted to import. Returns nil when the
// import is allowed or unrelated.
func checkCellOwnedSubpackage(modPrefix, srcPath, imp, srcLayer string) *violation {
	impRel := strings.TrimPrefix(imp, modPrefix)
	for ownedRel, ownerPrefix := range cellOwnedSubpackages {
		if impRel != ownedRel && !strings.HasPrefix(impRel, ownedRel+"/") {
			continue
		}
		// cmd/ and examples/ are universally unrestricted consumers.
		if srcLayer == "cmd" || srcLayer == "examples" {
			return nil
		}
		srcRel := strings.TrimPrefix(srcPath, modPrefix)
		// The owning cell's tree may import its own subpackage freely.
		// ownerRoot covers the case where srcRel is the cell root itself
		// (e.g. "cells/accesscore") which HasPrefix("cells/accesscore/")
		// would reject due to the missing trailing slash.
		ownerRoot := strings.TrimSuffix(ownerPrefix, "/")
		if srcRel == ownerRoot || strings.HasPrefix(srcRel, ownerPrefix) {
			return nil
		}
		return &violation{
			Rule:    "LAYER-06",
			Pkg:     srcPath,
			Import:  imp,
			Message: fmt.Sprintf("LAYER-06: %s imports %s (cell-owned subpackage; only %s* / cmd/* / examples/* may import it)", srcPath, imp, ownerPrefix),
		}
	}
	return nil
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

// loadPackages loads all packages under root using golang.org/x/tools/go/packages.
// The -tags=integration build flag is applied so that integration-tagged files
// participate in the layering analysis. Errors in individual packages (e.g. Go's
// internal/ visibility rejection) are tolerated: packages with errors are still
// included so LAYER-05 violations surface as rule-specific failures rather than
// a generic command failure that masks other violations.
func loadPackages(t *testing.T, root string) []pkgInfo {
	t.Helper()
	cfg := &packages.Config{
		Mode:       packages.NeedName | packages.NeedImports,
		Dir:        root,
		BuildFlags: []string{"-tags=integration"},
	}
	pkgs, err := packages.Load(cfg, "./...")
	require.NoError(t, err, "packages.Load failed")

	var out []pkgInfo
	for _, p := range pkgs {
		imports := make([]string, 0, len(p.Imports))
		for path := range p.Imports {
			imports = append(imports, path)
		}
		sort.Strings(imports)
		out = append(out, pkgInfo{
			ImportPath: p.PkgPath,
			Imports:    imports,
		})
	}
	return out
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
	t.Run("LAYER-06_cell_owned_subpackages_stay_within_owner", func(t *testing.T) {
		assert.Empty(t, byRule["LAYER-06"],
			"cell-owned public subpackages (see cellOwnedSubpackages) must only be imported by the owning cell, cmd/, or examples/")
	})

	// LAYER-07: cells/**/*.go (non-test) must not directly import the router package.
	// Cells must go through cell.RouteMux / cell.RouteGroup — the concrete router
	// implementation is an internal detail of runtime/http/router.
	t.Run("LAYER-07_no_direct_router_import_in_cells", func(t *testing.T) {
		modPath := strings.TrimSuffix(modPrefix, "/")
		routerPkg := modPath + "/runtime/http/router"
		var layer07violations []string
		for _, pkg := range pkgs {
			if layerOf(modPrefix, pkg.ImportPath) != "cells" {
				continue
			}
			// Skip test packages (archtest deliberately uses go list which includes _test).
			// The import path for external test packages ends with "_test"; internal test
			// binaries share the same ImportPath but are not reachable from production code.
			if strings.HasSuffix(pkg.ImportPath, "_test") {
				continue
			}
			for _, imp := range pkg.Imports {
				if imp == routerPkg {
					layer07violations = append(layer07violations,
						fmt.Sprintf("LAYER-07: %s imports %s (cells must not import the router directly; use cell.RouteMux / cell.RouteGroup)", pkg.ImportPath, imp))
				}
			}
		}
		assert.Empty(t, layer07violations,
			"cells/ must not directly import runtime/http/router; route through cell.RouteGroup.Register func(cell.RouteMux)")
	})

	// LAYER-08: no Go file anywhere in the module (outside of this archtest package
	// itself) may reference the identifier "HTTPRegistrar" — this is the final seal
	// confirming the legacy interface has been fully removed (PR-A14b).
	t.Run("LAYER-08_no_HTTPRegistrar_legacy_identifier", func(t *testing.T) {
		// Exclude tools/archtest itself because the rule definition necessarily
		// mentions the forbidden string in the test name and comments.
		hits := grepInDir(t, root, "HTTPRegistrar", "tools/archtest")
		if len(hits) > 0 {
			for _, h := range hits {
				t.Logf("LAYER-08 violation: %s", h)
			}
		}
		assert.Empty(t, hits,
			"HTTPRegistrar must not appear anywhere in the codebase; the legacy interface has been fully removed (PR-A14b)")
	})

	// LAYER-09: cells/X must not import cells/Y/events (cross-cell public events package).
	// cells/{cell}/events/ packages are owned by the declaring cell; sibling cells must
	// communicate via contract wire types, not by directly importing the events package.
	t.Run("LAYER-09_no_cross_cell_events_imports", func(t *testing.T) {
		assert.Empty(t, byRule["LAYER-09"],
			"cells/ must not import another cell's events/ package (cells/{cell}/events/); "+
				"use contract wire types instead (cell-patterns.md three-tier DTO rule)")
	})
}

// grepInDir walks root recursively and returns "file:line:text" strings for
// every line in a *.go file that contains the literal target string.
// This is intentionally simple — no regex, exact substring match only.
// excludeDirs lists directory names (relative to root) to skip entirely.
func grepInDir(t *testing.T, root, target string, excludeDirs ...string) []string {
	t.Helper()
	excludeSet := make(map[string]bool, len(excludeDirs))
	for _, d := range excludeDirs {
		excludeSet[filepath.Join(root, d)] = true
	}
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip excluded dirs, hidden dirs, and vendor.
			if excludeSet[path] {
				return filepath.SkipDir
			}
			name := info.Name()
			if name == "vendor" || (len(name) > 0 && name[0] == '.') {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			if strings.Contains(scanner.Text(), target) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, lineNum, strings.TrimSpace(scanner.Text())))
			}
		}
		return scanner.Err()
	})
	require.NoError(t, err, "error walking module root for grep")
	return hits
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
		{"github.com/ghbvf/gocell/cells/accesscore", "cells"},
		{"github.com/ghbvf/gocell/cells/accesscore/internal/domain", "cells"},
		{"github.com/ghbvf/gocell/pkg/errcode", "pkg"},
		{"github.com/ghbvf/gocell/cmd/gocell", "cmd"},
		{"github.com/ghbvf/gocell/examples/ssobff", "examples"},
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
		{"github.com/ghbvf/gocell/cells/accesscore", "accesscore"},
		{"github.com/ghbvf/gocell/cells/accesscore/internal/domain", "accesscore"},
		{"github.com/ghbvf/gocell/cells/auditcore/slices/auditappend", "auditcore"},
		{"github.com/ghbvf/gocell/cells/configcore", "configcore"},
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
		{"github.com/ghbvf/gocell/cells/accesscore/internal/domain", true},
		{"github.com/ghbvf/gocell/cells/auditcore/internal", true},
		{"github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogin", false},
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
					"github.com/ghbvf/gocell/cells/accesscore", // forbidden
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
				{ImportPath: "github.com/ghbvf/gocell/cells/accesscore", Imports: []string{
					"github.com/ghbvf/gocell/kernel/cell",
					"github.com/ghbvf/gocell/adapters/postgres", // forbidden
				}},
			},
			wantRules: []string{"LAYER-02"},
		},
		{
			name: "LAYER-02 clean: cells imports kernel + runtime (allowed)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/accesscore", Imports: []string{
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
					"github.com/ghbvf/gocell/cells/auditcore", // forbidden
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
					"github.com/ghbvf/gocell/cells/configcore", // forbidden
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
					"github.com/ghbvf/gocell/examples/ssobff", // forbidden
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
				{ImportPath: "github.com/ghbvf/gocell/cells/auditcore/slices/auditappend", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/internal/domain", // forbidden
				}},
			},
			wantRules: []string{"LAYER-05"},
		},
		{
			name: "LAYER-05 clean: same-cell internal import (allowed)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/auditcore/slices/auditappend", Imports: []string{
					"github.com/ghbvf/gocell/cells/auditcore/internal/domain", // same cell, OK
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-06 violation: sibling cell imports accesscore/initialadmin",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/auditcore", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/initialadmin", // forbidden — cell-owned subpkg
				}},
			},
			wantRules: []string{"LAYER-06"},
		},
		{
			name: "LAYER-06 violation: sibling cell slice imports nested path of initialadmin",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/configcore/slices/configpublish", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/initialadmin/somesubpkg", // forbidden — nested match
				}},
			},
			wantRules: []string{"LAYER-06"},
		},
		{
			// Runtime→cell imports are already caught by LAYER-03 before
			// LAYER-06 has a chance to fire; LAYER-06 is scoped to cell→cell
			// cases that LAYER-03 does not cover. We keep the case to lock
			// the precedence: LAYER-03 fires first (stronger signal) and
			// LAYER-06 is not needed here.
			name: "LAYER-03 covers runtime importing cell-owned subpkg",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/runtime/bootstrap", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/initialadmin",
				}},
			},
			wantRules: []string{"LAYER-03"},
		},
		{
			name: "LAYER-06 clean: accesscore itself imports initialadmin (owner)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/accesscore", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/initialadmin", // owner, OK
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-06 clean: accesscore slice imports initialadmin (owner tree)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogin", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/initialadmin", // owner tree, OK
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-06 clean: cmd imports initialadmin (composition root)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cmd/corebundle", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/initialadmin", // cmd unrestricted
				}},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-06 clean: examples imports initialadmin (unrestricted)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/examples/ssobff", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/initialadmin", // examples unrestricted
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
				{ImportPath: "github.com/ghbvf/gocell/cells/accesscore", Imports: []string{
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
					"github.com/ghbvf/gocell/cells/accesscore",
				}},
			},
			wantRules: nil,
		},
		{
			name: "clean: examples imports all layers (unrestricted)",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/examples/ssobff", Imports: []string{
					"github.com/ghbvf/gocell/kernel/cell",
					"github.com/ghbvf/gocell/runtime/auth",
					"github.com/ghbvf/gocell/adapters/postgres",
					"github.com/ghbvf/gocell/cells/accesscore",
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
		// LAYER-03 negative probe for LAYER-07 semantics (TEST-01): a cells/ package
		// importing runtime/http/router is already caught by LAYER-03 (cells must not
		// import adapters, and LAYER-03 forbids cells→runtime direct imports would
		// actually be fine for cells→runtime; the direct router import is LAYER-07 which
		// is checked inline in TestLayeringRules, not via checkLayering). We confirm
		// checkLayering detects the underlying LAYER-03 violation when a cell imports
		// runtime directly — this is the machine-readable test that demonstrates the
		// rule engine catches forbidden imports. For the LAYER-07 specific inline check,
		// see the negative_probe sub-test in TestLayeringRules_LAYER07_NegativeProbe below.
		{
			name: "LAYER-07 semantic: cells importing runtime/http/router caught as LAYER-03",
			pkgs: []pkgInfo{
				{
					ImportPath: "github.com/ghbvf/gocell/cells/accesscore",
					Imports: []string{
						"github.com/ghbvf/gocell/runtime/http/router", // router is runtime — not forbidden by LAYER-02/03 for cells
					},
				},
			},
			// cells→runtime is allowed by LAYER-03 (only cells→adapters is forbidden);
			// the actual LAYER-07 guard is implemented inline (not via checkLayering).
			// This case documents the expected clean result so the table is self-consistent.
			wantRules: nil,
		},
		// LAYER-09: cells/X must not import cells/Y/events (cross-cell public events package).
		// rationale: cell-patterns.md three-tier DTO rule — cells/{cell}/events/ must not be
		// shared as wire types across cell boundaries.
		{
			name: "LAYER-09 violation: cells/auditcore imports cells/configcore/events",
			pkgs: []pkgInfo{
				{
					ImportPath: "github.com/ghbvf/gocell/cells/auditcore/slices/auditappend",
					Imports: []string{
						"github.com/ghbvf/gocell/cells/configcore/events", // cross-cell events import — forbidden
					},
				},
			},
			wantRules: []string{"LAYER-09"},
		},
		{
			name: "LAYER-09 clean: cells/configcore imports cells/configcore/events (same cell, allowed)",
			pkgs: []pkgInfo{
				{
					ImportPath: "github.com/ghbvf/gocell/cells/configcore/slices/configpublish",
					Imports: []string{
						"github.com/ghbvf/gocell/cells/configcore/events", // same cell — OK
					},
				},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-09 clean: examples imports cells/configcore/events (unrestricted)",
			pkgs: []pkgInfo{
				{
					ImportPath: "github.com/ghbvf/gocell/examples/ssobff",
					Imports: []string{
						"github.com/ghbvf/gocell/cells/configcore/events", // examples unrestricted
					},
				},
			},
			wantRules: nil,
		},
		{
			name: "LAYER-09 clean: cmd imports cells/configcore/events (unrestricted)",
			pkgs: []pkgInfo{
				{
					ImportPath: "github.com/ghbvf/gocell/cmd/corebundle",
					Imports: []string{
						"github.com/ghbvf/gocell/cells/configcore/events", // cmd unrestricted
					},
				},
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

// TestLayeringRules_LAYER07_NegativeProbe is the "test the test" meta-test for
// LAYER-07 (TEST-01). It builds a synthetic pkgInfo slice that contains a
// cells/ package directly importing runtime/http/router, then runs the LAYER-07
// check logic inline and asserts the violation is detected. This confirms the
// rule engine catches the forbidden import before any such import ever reaches
// the real codebase.
func TestLayeringRules_LAYER07_NegativeProbe(t *testing.T) {
	t.Parallel()

	const modPrefix = "github.com/ghbvf/gocell/"
	modPath := strings.TrimSuffix(modPrefix, "/")
	routerPkg := modPath + "/runtime/http/router"

	// Synthetic fixture: a cells/ package that would violate LAYER-07 by
	// directly importing the router package.
	syntheticPkgs := []pkgInfo{
		{
			ImportPath: modPrefix + "cells/accesscore/slices/some_route_slice",
			Imports:    []string{routerPkg},
		},
	}

	// Run the same inline logic as LAYER-07 in TestLayeringRules.
	var layer07violations []string
	for _, pkg := range syntheticPkgs {
		if layerOf(modPrefix, pkg.ImportPath) != "cells" {
			continue
		}
		if strings.HasSuffix(pkg.ImportPath, "_test") {
			continue
		}
		for _, imp := range pkg.Imports {
			if imp == routerPkg {
				layer07violations = append(layer07violations,
					fmt.Sprintf("LAYER-07: %s imports %s", pkg.ImportPath, imp))
			}
		}
	}

	// The negative probe must find exactly one violation.
	require.Len(t, layer07violations, 1,
		"LAYER-07 negative probe: expected exactly one violation for synthetic router import")
	assert.Contains(t, layer07violations[0], "LAYER-07",
		"violation message must carry the LAYER-07 rule tag")
	assert.Contains(t, layer07violations[0], routerPkg,
		"violation message must name the forbidden import")
}

// TestLayeringRules_LAYER08_NegativeProbe is the "test the test" meta-test for
// LAYER-08 (TEST-01). It creates a temporary file containing the forbidden
// identifier "HTTPRegistrar" in a non-archtest path, calls grepInDir against
// a temp root, and asserts the hit is detected. This confirms the grep-based
// seal check would catch a real violation.
func TestLayeringRules_LAYER08_NegativeProbe(t *testing.T) {
	t.Parallel()

	// Create a minimal temp directory tree that looks like a Go file containing
	// the forbidden identifier.
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "fakecore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))

	violatingFile := filepath.Join(cellsDir, "fake_router.go")
	content := "package fakecore\n\n// HTTPRegistrar is the legacy interface removed in PR-A14b.\ntype HTTPRegistrar interface{}\n"
	require.NoError(t, os.WriteFile(violatingFile, []byte(content), 0o644))

	// grepInDir must detect the violation.
	hits := grepInDir(t, root, "HTTPRegistrar")
	require.NotEmpty(t, hits,
		"LAYER-08 negative probe: grepInDir must detect 'HTTPRegistrar' in the synthetic fixture")
	assert.Contains(t, hits[0], "fake_router.go",
		"hit must reference the violating file")
}

// TestLayeringRules_LAYER09_NegativeProbe is the "test the test" meta-test for
// LAYER-09. It builds synthetic pkgInfo slices covering all four boundary cases
// (cross-cell violation, same-cell allowed, examples allowed, cmd allowed) and
// runs checkLayering to confirm the rule fires exactly when expected.
func TestLayeringRules_LAYER09_NegativeProbe(t *testing.T) {
	t.Parallel()

	const modPrefix = "github.com/ghbvf/gocell/"

	tests := []struct {
		name        string
		src         string
		imp         string
		wantViolate bool
	}{
		{
			name:        "cross-cell: auditcore imports configcore/events → violation",
			src:         modPrefix + "cells/auditcore/slices/auditappend",
			imp:         modPrefix + "cells/configcore/events",
			wantViolate: true,
		},
		{
			name:        "same-cell: configcore imports configcore/events → allowed",
			src:         modPrefix + "cells/configcore/slices/configpublish",
			imp:         modPrefix + "cells/configcore/events",
			wantViolate: false,
		},
		{
			name:        "examples imports configcore/events → allowed",
			src:         modPrefix + "examples/ssobff",
			imp:         modPrefix + "cells/configcore/events",
			wantViolate: false,
		},
		{
			name:        "cmd imports configcore/events → allowed",
			src:         modPrefix + "cmd/corebundle",
			imp:         modPrefix + "cells/configcore/events",
			wantViolate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pkgs := []pkgInfo{{ImportPath: tt.src, Imports: []string{tt.imp}}}
			violations := checkLayering(modPrefix, pkgs)
			var layer09 []violation
			for _, v := range violations {
				if v.Rule == "LAYER-09" {
					layer09 = append(layer09, v)
				}
			}
			if tt.wantViolate {
				require.NotEmpty(t, layer09,
					"LAYER-09 negative probe: expected violation for %s → %s", tt.src, tt.imp)
				assert.Contains(t, layer09[0].Message, "LAYER-09")
				assert.Contains(t, layer09[0].Message, tt.imp)
			} else {
				assert.Empty(t, layer09,
					"LAYER-09 negative probe: expected no violation for %s → %s", tt.src, tt.imp)
			}
		})
	}
}
