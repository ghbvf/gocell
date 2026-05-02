package archtest

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/depgraph"
)

// readModulePath parses go.mod to extract the module path (e.g. "github.com/ghbvf/gocell").
// This avoids hardcoding the module path, which would silently disable all rules on rename or /v2 bump.
func readModulePath(t *testing.T, modRoot string) string {
	t.Helper()
	f, err := os.Open(filepath.Clean(filepath.Join(modRoot, "go.mod")))
	require.NoError(t, err, "cannot open go.mod")
	defer func() { require.NoError(t, f.Close()) }()

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

// layerOf is a backward-compatible shim around depgraph.LayerOf that keeps
// archtest's "external returns empty string" convention. modPrefix must
// include the trailing slash (e.g. "github.com/ghbvf/gocell/"). Internal
// packages return their layer name; stdlib / third-party / root collapse
// to "" so existing skip-on-empty branches in checkLayering keep working.
//
// The single source of truth for layer classification is now
// tools/depgraph/layer.go; this shim only adapts the signature.
func layerOf(modPrefix, importPath string) string {
	module := strings.TrimSuffix(modPrefix, "/")
	switch layer := depgraph.LayerOf(module, importPath); layer {
	case depgraph.LayerStdlib, depgraph.LayerThirdParty, depgraph.LayerRoot, "":
		return ""
	default:
		return layer
	}
}

// cellOf delegates to depgraph.CellOf with archtest's modPrefix convention.
func cellOf(modPrefix, importPath string) string {
	return depgraph.CellOf(strings.TrimSuffix(modPrefix, "/"), importPath)
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
	"cells/accesscore/configgetter": "cells/accesscore/",
	"cells/accesscore/initialadmin": "cells/accesscore/",
	"cells/configcore/postgres":     "cells/configcore/",
}

// checkLayering runs 4 metadata-aware layering rules (LAYER-05/06/09/10).
// LAYER-01..04 path rules are owned by depguard in `.golangci.yml`.
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
			if isRootCellPackage(modPrefix, pkg.ImportPath) && srcCell != "" {
				impRel := strings.TrimPrefix(imp, modPrefix)
				internalAdaptersPrefix := "cells/" + srcCell + "/internal/adapters/"
				if strings.HasPrefix(impRel, internalAdaptersPrefix) {
					out = append(out, violation{
						Rule:    "LAYER-10",
						Pkg:     pkg.ImportPath,
						Import:  imp,
						Message: fmt.Sprintf("LAYER-10: %s imports %s (root cell package must not construct concrete adapters)", pkg.ImportPath, imp),
					})
				}
			}

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
			Rule:   "LAYER-06",
			Pkg:    srcPath,
			Import: imp,
			Message: fmt.Sprintf(
				"LAYER-06: %s imports %s (cell-owned subpackage; only %s* / cmd/* / examples/* may import it)",
				srcPath, imp, ownerPrefix),
		}
	}
	return nil
}

func isRootCellPackage(modPrefix, importPath string) bool {
	cellsPrefix := modPrefix + "cells/"
	if !strings.HasPrefix(importPath, cellsPrefix) {
		return false
	}
	rel := strings.TrimPrefix(importPath, cellsPrefix)
	return rel != "" && !strings.Contains(rel, "/") && !strings.HasSuffix(rel, "_test")
}

func isCellPublicAPIDisallowedType(modPrefix, pkgPath string) bool {
	module := strings.TrimSuffix(modPrefix, "/")
	if strings.HasPrefix(pkgPath, module+"/adapters/") {
		return true
	}
	for _, prefix := range []string{
		"github.com/aws/aws-sdk-go-v2/",
		"github.com/jackc/pgx/",
		"github.com/prometheus/client_golang/prometheus",
		"github.com/rabbitmq/amqp091-go",
		"github.com/redis/go-redis/",
		"github.com/coder/websocket",
	} {
		if strings.HasPrefix(pkgPath, prefix) {
			return true
		}
	}
	return false
}

func findDisallowedTypePath(modPrefix string, typ types.Type) string {
	switch t := typ.(type) {
	case nil:
		return ""
	case *types.Basic:
		return ""
	case *types.Named:
		if obj := t.Obj(); obj != nil && obj.Pkg() != nil && isCellPublicAPIDisallowedType(modPrefix, obj.Pkg().Path()) {
			return obj.Pkg().Path()
		}
		typeArgs := t.TypeArgs()
		for i := 0; typeArgs != nil && i < typeArgs.Len(); i++ {
			if p := findDisallowedTypePath(modPrefix, typeArgs.At(i)); p != "" {
				return p
			}
		}
		return ""
	case *types.TypeParam:
		return findDisallowedTypePath(modPrefix, t.Constraint())
	case *types.Pointer:
		return findDisallowedTypePath(modPrefix, t.Elem())
	case *types.Slice:
		return findDisallowedTypePath(modPrefix, t.Elem())
	case *types.Array:
		return findDisallowedTypePath(modPrefix, t.Elem())
	case *types.Map:
		if p := findDisallowedTypePath(modPrefix, t.Key()); p != "" {
			return p
		}
		return findDisallowedTypePath(modPrefix, t.Elem())
	case *types.Chan:
		return findDisallowedTypePath(modPrefix, t.Elem())
	case *types.Signature:
		if p := findDisallowedTupleTypePath(modPrefix, t.Params()); p != "" {
			return p
		}
		return findDisallowedTupleTypePath(modPrefix, t.Results())
	case *types.Interface:
		for method := range t.ExplicitMethods() {
			if p := findDisallowedTypePath(modPrefix, method.Type()); p != "" {
				return p
			}
		}
		for etyp := range t.EmbeddedTypes() {
			if p := findDisallowedTypePath(modPrefix, etyp); p != "" {
				return p
			}
		}
		return ""
	case *types.Struct:
		for f := range t.Fields() {
			if !f.Exported() && !f.Anonymous() {
				continue
			}
			if p := findDisallowedTypePath(modPrefix, f.Type()); p != "" {
				return p
			}
		}
		return ""
	default:
		return ""
	}
}

func findDisallowedTupleTypePath(modPrefix string, tuple *types.Tuple) string {
	if tuple == nil {
		return ""
	}
	for v := range tuple.Variables() {
		if p := findDisallowedTypePath(modPrefix, v.Type()); p != "" {
			return p
		}
	}
	return ""
}

func layer10IncompleteTypeDataViolation(pkgPath, detail string) violation {
	return violation{
		Rule:    "LAYER-10",
		Pkg:     pkgPath,
		Message: fmt.Sprintf("LAYER-10: %s typed package load incomplete: %s", pkgPath, detail),
	}
}

func checkCellPublicAPIAdapterTypes(modPrefix string, pkgs []*packages.Package) []violation {
	var out []violation
	for _, pkg := range pkgs {
		if !isRootCellPackage(modPrefix, pkg.PkgPath) {
			continue
		}
		for _, pe := range pkg.Errors {
			out = append(out, layer10IncompleteTypeDataViolation(pkg.PkgPath,
				fmt.Sprintf("package load/type error: %v", pe)))
		}
		if pkg.Types == nil {
			out = append(out, layer10IncompleteTypeDataViolation(pkg.PkgPath, "missing Types"))
			continue
		}
		if pkg.TypesInfo == nil {
			out = append(out, layer10IncompleteTypeDataViolation(pkg.PkgPath, "missing TypesInfo"))
			continue
		}
		if len(pkg.Syntax) == 0 {
			out = append(out, layer10IncompleteTypeDataViolation(pkg.PkgPath, "missing syntax"))
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					symbol := d.Name.Name
					if d.Recv != nil {
						symbol = "method " + symbol
					}
					obj := pkg.TypesInfo.Defs[d.Name]
					if obj == nil {
						out = append(out, layer10IncompleteTypeDataViolation(pkg.PkgPath,
							fmt.Sprintf("missing type info for exported API %s", symbol)))
						continue
					}
					if p := findDisallowedTypePath(modPrefix, obj.Type()); p != "" {
						out = append(out, violation{
							Rule:    "LAYER-10",
							Pkg:     pkg.PkgPath,
							Import:  p,
							Message: fmt.Sprintf("LAYER-10: %s exposes adapter/driver type %s in exported API %s", pkg.PkgPath, p, symbol),
						})
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if !s.Name.IsExported() {
								continue
							}
							typ := pkg.TypesInfo.TypeOf(s.Type)
							if typ == nil {
								out = append(out, layer10IncompleteTypeDataViolation(pkg.PkgPath,
									fmt.Sprintf("missing type info for exported type %s", s.Name.Name)))
								continue
							}
							if p := findDisallowedTypePath(modPrefix, typ); p != "" {
								out = append(out, violation{
									Rule:    "LAYER-10",
									Pkg:     pkg.PkgPath,
									Import:  p,
									Message: fmt.Sprintf("LAYER-10: %s exposes adapter/driver type %s in exported type %s", pkg.PkgPath, p, s.Name.Name),
								})
							}
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if !name.IsExported() {
									continue
								}
								obj := pkg.TypesInfo.Defs[name]
								if obj == nil {
									out = append(out, layer10IncompleteTypeDataViolation(pkg.PkgPath,
										fmt.Sprintf("missing type info for exported var/const %s", name.Name)))
									continue
								}
								if p := findDisallowedTypePath(modPrefix, obj.Type()); p != "" {
									out = append(out, violation{
										Rule:    "LAYER-10",
										Pkg:     pkg.PkgPath,
										Import:  p,
										Message: fmt.Sprintf("LAYER-10: %s exposes adapter/driver type %s in exported var/const %s", pkg.PkgPath, p, name.Name),
									})
								}
							}
						}
					}
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

// loadDepGraph loads the entire module under root once via depgraph.Load
// with the integration build tag, returning the typed graph. The same
// loaded packages back both the dep-graph view (for direct + transitive
// layer rules) and the *packages.Package slice (for LAYER-08 type-scope
// scan and LAYER-10 cell-API adapter check). Replaces the previous
// loadPackages + loadTypedPackages double-load in TestLayeringRules.
func loadDepGraph(t *testing.T, root string) *depgraph.Graph {
	t.Helper()
	g, err := depgraph.Load(depgraph.LoadOptions{
		Dir:       root,
		BuildTags: []string{"integration"},
	}, "./...")
	require.NoError(t, err, "depgraph.Load failed")
	for _, p := range g.RawPackages() {
		for _, pe := range p.Errors {
			t.Logf("depgraph.Load: package %q error: %v", p.PkgPath, pe)
		}
	}
	return g
}

// loadTypedPackages loads a specific subset of patterns with full type
// info. Used by tests that scope to a slice of the module
// (kernel/lifecycle + adapters/, etc.) where the full module graph would
// be wasteful. TestLayeringRules uses loadDepGraph instead.
func loadTypedPackages(t *testing.T, root string, patterns ...string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode:       packages.NeedName | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:        root,
		BuildFlags: []string{"-tags=integration"},
	}
	pkgs, err := packages.Load(cfg, patterns...)
	require.NoError(t, err, "packages.Load failed")
	for _, p := range pkgs {
		for _, pe := range p.Errors {
			t.Logf("loadTypedPackages: package %q error: %v", p.PkgPath, pe)
		}
	}
	return pkgs
}

// pkgInfosFromGraph converts a graph's nodes into the lightweight pkgInfo
// shape consumed by checkLayering and the negative-probe tests.
func pkgInfosFromGraph(g *depgraph.Graph) []pkgInfo {
	out := make([]pkgInfo, 0, len(g.Packages))
	for _, n := range g.Packages {
		out = append(out, pkgInfo{
			ImportPath: n.ID,
			Imports:    append([]string(nil), n.Imports...),
		})
	}
	return out
}

// --- integration test (real go/packages data via depgraph) ---

func TestLayeringRules(t *testing.T) {
	root := findModuleRoot(t)
	modPrefix := readModulePath(t, root) + "/"
	module := strings.TrimSuffix(modPrefix, "/")

	g := loadDepGraph(t, root)
	pkgs := pkgInfosFromGraph(g)
	require.NotEmpty(t, pkgs, "depgraph returned no packages")

	violations := checkLayering(modPrefix, pkgs)

	// Group violations by rule for readable output.
	byRule := map[string][]string{}
	for _, v := range violations {
		byRule[v.Rule] = append(byRule[v.Rule], v.Message)
	}

	// Summary log for quick diagnosis when multiple rules are violated.
	if len(violations) > 0 {
		t.Logf("Found %d direct-import layering violation(s):", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v.Message)
		}
	}

	// LAYER-01..04 are path-level rules owned by depguard in .golangci.yml.
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
		routerPkg := module + "/runtime/http/router"
		var layer07violations []string
		for _, pkg := range pkgs {
			if layerOf(modPrefix, pkg.ImportPath) != "cells" {
				continue
			}
			if strings.HasSuffix(pkg.ImportPath, "_test") {
				continue
			}
			for _, imp := range pkg.Imports {
				if imp == routerPkg {
					layer07violations = append(layer07violations,
						fmt.Sprintf(
							"LAYER-07: %s imports %s (cells must not import the router directly;"+
								" use cell.RouteMux / cell.RouteGroup)",
							pkg.ImportPath, imp))
				}
			}
		}
		assert.Empty(t, layer07violations,
			"cells/ must not directly import runtime/http/router; route through cell.RouteGroup.Register func(cell.RouteMux)")
	})

	// LAYER-08: the legacy HTTPRegistrar interface must remain removed
	// (PR-A14b). This is enforced at the type level: if any package in the
	// module declares a top-level type named HTTPRegistrar, flag it.
	// Type-level scope walk is precise where the previous file-grep
	// over-matched on comments and missed renamed-import aliases.
	t.Run("LAYER-08_no_HTTPRegistrar_type_definition", func(t *testing.T) {
		violations := checkLayer08TypedSeal(module, g.RawPackages())
		for _, v := range violations {
			t.Logf("LAYER-08 violation: %s", v.Message)
		}
		assert.Empty(t, violations,
			"HTTPRegistrar must not be defined in any module package; the legacy interface remains removed (PR-A14b)")
	})

	// LAYER-09: cells/X must not import cells/Y/events (cross-cell public events package).
	t.Run("LAYER-09_no_cross_cell_events_imports", func(t *testing.T) {
		assert.Empty(t, byRule["LAYER-09"],
			"cells/ must not import another cell's events/ package (cells/{cell}/events/); "+
				"use contract wire types instead (cell-patterns.md three-tier DTO rule)")
	})

	// LAYER-10: cells/<cell> root package exported APIs must not expose
	// concrete adapter/driver types.
	t.Run("LAYER-10_cell_root_public_api_no_adapter_driver_types", func(t *testing.T) {
		typedCellPkgs := filterCellPackages(module, g.RawPackages())
		violations := checkCellPublicAPIAdapterTypes(modPrefix, typedCellPkgs)
		for _, v := range violations {
			t.Logf("LAYER-10 violation: %s", v.Message)
		}
		assert.Empty(t, violations,
			"cells/<cell> exported APIs must not expose concrete adapter/driver types; "+
				"move adapter-specific factories into composition-root owned wiring or cell-owned adapter subpackages")
	})

	// --- transitive-closure variants (NEW in PR-V1-DEPGRAPH-TYPED-ARCHTEST) ---
	//
	// The direct-import rules above catch a cell A → cell B/internal edge.
	// They do not catch laundering: A → utility → B/internal. The T-suffix
	// rules walk depgraph.TransitiveImports to flag indirect violations as
	// well. False-positive avoidance: TransitiveImports already filters
	// stdlib / third-party / test-only nodes (closure stays inside the
	// module on production edges).

	t.Run("LAYER-05T_no_transitive_cross_cell_internal_imports", func(t *testing.T) {
		violations := checkTransitiveCrossCellInternal(module, g)
		for _, v := range violations {
			t.Logf("LAYER-05T violation: %s", v.Message)
		}
		assert.Empty(t, violations,
			"cells must not transitively reach another cell's internal/ packages")
	})

	t.Run("LAYER-06T_no_transitive_cell_owned_subpackage_imports", func(t *testing.T) {
		violations := checkTransitiveCellOwnedSubpackage(modPrefix, g)
		for _, v := range violations {
			t.Logf("LAYER-06T violation: %s", v.Message)
		}
		assert.Empty(t, violations,
			"cells must not transitively reach a cell-owned public subpackage of a sibling cell")
	})

	t.Run("LAYER-09T_no_transitive_cross_cell_events_imports", func(t *testing.T) {
		violations := checkTransitiveCrossCellEvents(module, g)
		for _, v := range violations {
			t.Logf("LAYER-09T violation: %s", v.Message)
		}
		assert.Empty(t, violations,
			"cells must not transitively reach another cell's events/ package")
	})
}

// filterCellPackages returns the subset of pkgs whose path is under
// <module>/cells/. Replaces the old `loadTypedPackages(t, root, "./cells/...")`
// double-load pattern.
func filterCellPackages(module string, pkgs []*packages.Package) []*packages.Package {
	prefix := module + "/cells/"
	out := pkgs[:0:0]
	for _, p := range pkgs {
		if p == nil {
			continue
		}
		if strings.HasPrefix(p.PkgPath, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// checkLayer08TypedSeal walks every loaded package's type scope and
// returns a violation for each top-level TypeName named "HTTPRegistrar".
// Since Go's type system requires definitions before use, the absence of
// any such definition implies the absence of any reference. Excludes the
// archtest package itself (which mentions the name in test fixtures and
// rule docs as scope-walked-but-string-only matches).
func checkLayer08TypedSeal(module string, pkgs []*packages.Package) []violation {
	archtestPkg := module + "/tools/archtest"
	var out []violation
	for _, p := range pkgs {
		if p == nil || p.Types == nil {
			continue
		}
		if p.PkgPath == archtestPkg {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok || tn.Name() != "HTTPRegistrar" {
				continue
			}
			out = append(out, violation{
				Rule: "LAYER-08",
				Pkg:  p.PkgPath,
				Message: fmt.Sprintf(
					"LAYER-08: %s declares type HTTPRegistrar (legacy interface must remain removed; PR-A14b)",
					p.PkgPath),
			})
		}
	}
	return out
}

// checkTransitiveCrossCellInternal flags every cell A whose transitive
// import closure reaches cells/B/internal/... for any B != A.
func checkTransitiveCrossCellInternal(module string, g *depgraph.Graph) []violation {
	var out []violation
	for _, src := range g.Packages {
		if src.Layer != depgraph.LayerCells || src.CellID == "" {
			continue
		}
		closure := g.TransitiveImports(src.ID)
		for dep := range closure {
			depCell := depgraph.CellOf(module, dep)
			if depCell == "" || depCell == src.CellID {
				continue
			}
			if !isInternal(dep) {
				continue
			}
			out = append(out, violation{
				Rule:   "LAYER-05T",
				Pkg:    src.ID,
				Import: dep,
				Message: fmt.Sprintf(
					"LAYER-05T: %s transitively reaches %s (cross-cell internal via closure)",
					src.ID, dep),
			})
		}
	}
	return out
}

// checkTransitiveCellOwnedSubpackage flags every cell A whose transitive
// import closure reaches a cell-owned public subpackage of a sibling cell.
// The exemption rules mirror checkCellOwnedSubpackage (cmd/ and examples/
// are unrestricted; the owning cell may import freely).
func checkTransitiveCellOwnedSubpackage(modPrefix string, g *depgraph.Graph) []violation {
	var out []violation
	for _, src := range g.Packages {
		srcLayer := layerOf(modPrefix, src.ID)
		if srcLayer == "cmd" || srcLayer == "examples" {
			continue
		}
		closure := g.TransitiveImports(src.ID)
		for dep := range closure {
			if v := checkCellOwnedSubpackage(modPrefix, src.ID, dep, srcLayer); v != nil {
				transV := *v
				transV.Rule = "LAYER-06T"
				transV.Message = strings.Replace(transV.Message, "LAYER-06:", "LAYER-06T (transitive):", 1)
				out = append(out, transV)
			}
		}
	}
	return out
}

// checkTransitiveCrossCellEvents flags every cell A whose transitive
// import closure reaches cells/B/events for any B != A.
func checkTransitiveCrossCellEvents(module string, g *depgraph.Graph) []violation {
	var out []violation
	for _, src := range g.Packages {
		if src.Layer != depgraph.LayerCells || src.CellID == "" {
			continue
		}
		closure := g.TransitiveImports(src.ID)
		for dep := range closure {
			depCell := depgraph.CellOf(module, dep)
			if depCell == "" || depCell == src.CellID {
				continue
			}
			eventsPrefix := module + "/cells/" + depCell + "/events"
			if dep != eventsPrefix && !strings.HasPrefix(dep, eventsPrefix+"/") {
				continue
			}
			out = append(out, violation{
				Rule:   "LAYER-09T",
				Pkg:    src.ID,
				Import: dep,
				Message: fmt.Sprintf(
					"LAYER-09T: %s transitively reaches %s (cross-cell events via closure)",
					src.ID, dep),
			})
		}
	}
	return out
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

func TestIsRootCellPackage(t *testing.T) {
	const mod = "github.com/ghbvf/gocell/"
	tests := []struct {
		input string
		want  bool
	}{
		{"github.com/ghbvf/gocell/cells/configcore", true},
		{"github.com/ghbvf/gocell/cells/accesscore", true},
		{"github.com/ghbvf/gocell/cells/configcore/postgres", false},
		{"github.com/ghbvf/gocell/cells/configcore/internal/ports", false},
		{"github.com/ghbvf/gocell/runtime/auth", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, isRootCellPackage(mod, tt.input))
		})
	}
}

func TestIsCellPublicAPIDisallowedType(t *testing.T) {
	const mod = "github.com/ghbvf/gocell/"
	tests := []struct {
		pkgPath string
		want    bool
	}{
		{"github.com/ghbvf/gocell/adapters/postgres", true},
		{"github.com/jackc/pgx/v5/pgxpool", true},
		{"github.com/redis/go-redis/v9", true},
		{"github.com/rabbitmq/amqp091-go", true},
		{"github.com/coder/websocket", true},
		{"github.com/prometheus/client_golang/prometheus", true},
		{"github.com/ghbvf/gocell/kernel/outbox", false},
	}
	for _, tt := range tests {
		t.Run(tt.pkgPath, func(t *testing.T) {
			assert.Equal(t, tt.want, isCellPublicAPIDisallowedType(mod, tt.pkgPath))
		})
	}
}

func TestCheckCellPublicAPIAdapterTypes_FindsViolations(t *testing.T) {
	const mod = "github.com/ghbvf/gocell/"
	rootPkg := types.NewPackage("github.com/ghbvf/gocell/cells/accesscore", "accesscore")
	poolPkg := types.NewPackage("github.com/jackc/pgx/v5/pgxpool", "pgxpool")
	promPkg := types.NewPackage("github.com/prometheus/client_golang/prometheus", "prometheus")

	poolType := types.NewNamed(types.NewTypeName(token.NoPos, poolPkg, "Pool", nil), types.NewStruct(nil, nil), nil)
	counterType := types.NewNamed(types.NewTypeName(token.NoPos, promPkg, "Counter", nil), types.NewInterfaceType(nil, nil).Complete(), nil)
	poolPtr := types.NewPointer(poolType)

	typeSpec := &ast.TypeSpec{Name: ast.NewIdent("ExportedStruct"), Type: ast.NewIdent("struct")}
	ifaceSpec := &ast.TypeSpec{Name: ast.NewIdent("ExportedInterface"), Type: ast.NewIdent("interface")}
	funcDecl := &ast.FuncDecl{Name: ast.NewIdent("WithPool"), Type: &ast.FuncType{}}
	varSpec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent("ExportedMetric")}}
	metricName := varSpec.Names[0]
	file := &ast.File{
		Name: ast.NewIdent("accesscore"),
		Decls: []ast.Decl{
			&ast.GenDecl{Specs: []ast.Spec{typeSpec}},
			&ast.GenDecl{Specs: []ast.Spec{ifaceSpec}},
			funcDecl,
			&ast.GenDecl{Specs: []ast.Spec{varSpec}},
		},
	}

	fakePkg := &packages.Package{
		PkgPath: "github.com/ghbvf/gocell/cells/accesscore",
		Syntax:  []*ast.File{file},
		Types:   rootPkg,
		TypesInfo: &types.Info{
			Defs:  map[*ast.Ident]types.Object{},
			Types: map[ast.Expr]types.TypeAndValue{},
		},
	}
	fakePkg.TypesInfo.Types[typeSpec.Type] = types.TypeAndValue{
		Type: types.NewStruct([]*types.Var{
			types.NewField(token.NoPos, rootPkg, "Pool", poolPtr, false),
		}, nil),
	}
	fakePkg.TypesInfo.Types[ifaceSpec.Type] = types.TypeAndValue{
		Type: types.NewInterfaceType([]*types.Func{
			types.NewFunc(token.NoPos, rootPkg, "Observe", types.NewSignatureType(nil, nil, nil,
				types.NewTuple(types.NewVar(token.NoPos, rootPkg, "counter", counterType)), nil, false)),
		}, nil).Complete(),
	}
	fakePkg.TypesInfo.Defs[funcDecl.Name] = types.NewFunc(token.NoPos, rootPkg, "WithPool",
		types.NewSignatureType(nil, nil, nil,
			types.NewTuple(types.NewVar(token.NoPos, rootPkg, "pool", poolPtr)), nil, false))
	fakePkg.TypesInfo.Defs[metricName] = types.NewVar(token.NoPos, rootPkg, "ExportedMetric", counterType)
	fakePkg.PkgPath = "github.com/ghbvf/gocell/cells/accesscore"

	violations := checkCellPublicAPIAdapterTypes(mod, []*packages.Package{fakePkg})

	var messages []string
	for _, v := range violations {
		messages = append(messages, v.Message)
	}
	assert.Len(t, violations, 4)
	assert.Contains(t, strings.Join(messages, "\n"), "exported type ExportedStruct")
	assert.Contains(t, strings.Join(messages, "\n"), "exported type ExportedInterface")
	assert.Contains(t, strings.Join(messages, "\n"), "exported API WithPool")
	assert.Contains(t, strings.Join(messages, "\n"), "exported var/const ExportedMetric")
}

func TestCheckCellPublicAPIAdapterTypes_FailsClosedOnIncompleteTypedPackage(t *testing.T) {
	const mod = "github.com/ghbvf/gocell/"
	rootPkg := types.NewPackage("github.com/ghbvf/gocell/cells/accesscore", "accesscore")
	funcDecl := &ast.FuncDecl{Name: ast.NewIdent("Exported"), Type: &ast.FuncType{}}
	file := &ast.File{
		Name:  ast.NewIdent("accesscore"),
		Decls: []ast.Decl{funcDecl},
	}

	loadErrorPkg := &packages.Package{
		PkgPath: "github.com/ghbvf/gocell/cells/accesscore",
		Syntax:  []*ast.File{file},
		Types:   rootPkg,
		TypesInfo: &types.Info{
			Defs: map[*ast.Ident]types.Object{
				funcDecl.Name: types.NewFunc(token.NoPos, rootPkg, "Exported",
					types.NewSignatureType(nil, nil, nil, nil, nil, false)),
			},
		},
		Errors: []packages.Error{{Msg: "undefined: broken"}},
	}
	missingObjectPkg := &packages.Package{
		PkgPath: "github.com/ghbvf/gocell/cells/configcore",
		Syntax:  []*ast.File{file},
		Types:   types.NewPackage("github.com/ghbvf/gocell/cells/configcore", "configcore"),
		TypesInfo: &types.Info{
			Defs: map[*ast.Ident]types.Object{},
		},
	}
	missingTypesInfoPkg := &packages.Package{
		PkgPath: "github.com/ghbvf/gocell/cells/auditcore",
		Syntax:  []*ast.File{file},
		Types:   types.NewPackage("github.com/ghbvf/gocell/cells/auditcore", "auditcore"),
	}

	violations := checkCellPublicAPIAdapterTypes(mod, []*packages.Package{
		loadErrorPkg,
		missingObjectPkg,
		missingTypesInfoPkg,
	})

	messages := make([]string, 0, len(violations))
	for _, v := range violations {
		messages = append(messages, v.Message)
	}
	got := strings.Join(messages, "\n")
	assert.Len(t, violations, 3)
	assert.Contains(t, got, "typed package load incomplete")
	assert.Contains(t, got, "missing type info for exported API Exported")
	assert.Contains(t, got, "missing TypesInfo")
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
	// Note: LAYER-01..04 path rules are owned by depguard in .golangci.yml.
	// Only LAYER-05/06/09/10 (metadata-aware rules) are tested here.
	tests := []struct {
		name      string
		pkgs      []pkgInfo
		wantRules []string // expected rule codes in violations
	}{
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
			name: "LAYER-06 violation: sibling cell imports configcore/postgres",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/auditcore", Imports: []string{
					"github.com/ghbvf/gocell/cells/configcore/postgres", // forbidden — cell-owned adapter subpkg
				}},
			},
			wantRules: []string{"LAYER-06"},
		},
		{
			name: "LAYER-10 violation: root cell imports own internal adapter",
			pkgs: []pkgInfo{
				{ImportPath: "github.com/ghbvf/gocell/cells/accesscore", Imports: []string{
					"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/http", // forbidden — hidden adapter factory
				}},
			},
			wantRules: []string{"LAYER-10"},
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
		// LAYER-07 path check: cells→runtime is not forbidden by checkLayering (LAYER-01..04
		// are now owned by depguard); the actual LAYER-07 guard is implemented inline in
		// TestLayeringRules. This case documents the expected clean result so the table is
		// self-consistent. For the LAYER-07 specific inline check, see
		// TestLayeringRules_LAYER07_NegativeProbe below.
		{
			name: "LAYER-07 semantic: cells importing runtime/http/router (checkLayering clean)",
			pkgs: []pkgInfo{
				{
					ImportPath: "github.com/ghbvf/gocell/cells/accesscore",
					Imports: []string{
						"github.com/ghbvf/gocell/runtime/http/router", // cells→runtime not caught by checkLayering
					},
				},
			},
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

// TestLayeringRules_LAYER08_NegativeProbe is the "test the test" meta-test
// for LAYER-08. It builds a synthetic *packages.Package with a
// types.Package whose top-level scope holds a TypeName named
// HTTPRegistrar, then asserts checkLayer08TypedSeal flags it. This
// confirms the typed seal catches a real violation.
func TestLayeringRules_LAYER08_NegativeProbe(t *testing.T) {
	t.Parallel()

	const module = "github.com/ghbvf/gocell"
	pkgPath := module + "/cells/fakecore"
	tp := types.NewPackage(pkgPath, "fakecore")
	tp.Scope().Insert(types.NewTypeName(token.NoPos, tp, "HTTPRegistrar", nil))

	violatingPkg := &packages.Package{
		PkgPath: pkgPath,
		Types:   tp,
	}

	violations := checkLayer08TypedSeal(module, []*packages.Package{violatingPkg})
	require.Len(t, violations, 1,
		"LAYER-08 negative probe: typed seal must flag synthetic HTTPRegistrar declaration")
	assert.Contains(t, violations[0].Message, "HTTPRegistrar")
	assert.Equal(t, pkgPath, violations[0].Pkg)
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

// TestLoadDepGraph_IntegrationTagPlumbing verifies that loadDepGraph passes
// -tags=integration so integration-tagged files participate in the layering
// analysis. The archtest package itself is always loadable; its presence in
// the graph is the sanity check that the build flags reached packages.Load.
func TestLoadDepGraph_IntegrationTagPlumbing(t *testing.T) {
	root := findModuleRoot(t)
	g := loadDepGraph(t, root)
	require.NotEmpty(t, g.Packages, "depgraph.Load must return packages; empty result means -tags=integration broke the load")

	modPrefix := readModulePath(t, root) + "/"
	archtestPkg := modPrefix + "tools/archtest"
	if g.ByID(archtestPkg) == nil {
		t.Errorf("tools/archtest package must appear in depgraph output (confirms -tags=integration did not break load)")
	}
}

// TestCorebundleMainLineLimit guards V-A8 (CMD-THICK-ENTRY-REDUCE) — the
// corebundle entry point is generated by `gocell generate assembly`, so the
// thinness verdict is enforced on the generator output. The 30-line ceiling
// gives a 2-line headroom over the current generator template (28 lines) so
// that benign comment/blank-line drift does not break CI; any structural
// growth (extra fields, helpers, init functions) must trip this and force a
// re-evaluation of V-A8 against its deferred-decision triggers
// (corebundle subpackage extraction, internalGuard public exposure).
// countLines reports the number of lines a Go source file would render as,
// matching the convention that an empty file is 0 lines and a no-trailing-
// newline file with content still counts its last line. Extracted so the
// boundary cases (empty / no-final-newline / typical trailing-newline) can
// be locked independently of cmd/corebundle/main.go's actual content.
func countLines(data []byte) int {
	n := bytes.Count(data, []byte("\n"))
	if !bytes.HasSuffix(data, []byte("\n")) && len(data) > 0 {
		n++
	}
	return n
}

func TestCountLines_Boundaries(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want int
	}{
		{"empty file is zero lines", []byte{}, 0},
		{"single line without trailing newline", []byte("package main"), 1},
		{"single line with trailing newline", []byte("package main\n"), 1},
		{"two lines with trailing newline", []byte("a\nb\n"), 2},
		{"two lines no trailing newline", []byte("a\nb"), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, countLines(tc.in))
		})
	}
}

func TestCorebundleMainLineLimit(t *testing.T) {
	const maxLines = 30
	root := findModuleRoot(t)
	path := filepath.Join(root, "cmd", "corebundle", "main.go")
	data, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err, "read %s", path)
	lines := countLines(data)
	assert.LessOrEqualf(t, lines, maxLines,
		"cmd/corebundle/main.go has %d lines, exceeds V-A8 ceiling of %d; "+
			"re-evaluate V-A8-DEFERRED triggers in docs/backlog.md and "+
			"docs/plans/202604252100-026-post-v1.0-cleanup-plan.md before raising the limit",
		lines, maxLines)
}
