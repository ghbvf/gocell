package depgraph

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// loadMode is the packages.Load mode required to build a Graph. depgraph
// only needs the structural fields (PkgPath, Imports, Module); type-level
// analysis lives in archtest's typeseval package and runs its own Load.
// Keeping this mode lean avoids forcing CLI / Track J consumers to pay
// the 3-10x cost of NeedSyntax/NeedTypes/NeedTypesInfo/NeedDeps.
const loadMode = packages.NeedName |
	packages.NeedImports |
	packages.NeedModule

// LoadOptions configures Load.
type LoadOptions struct {
	// IncludeTests, when true, sets packages.Config.Tests so test variants
	// of each package are loaded. Production-only closure analysis still
	// excludes test-variant edges, but TestOnly markings on Node become
	// meaningful (a node is TestOnly if no production package imports it).
	IncludeTests bool

	// BuildTags is joined as `-tags=a,b,c` and passed to packages.Config.
	// Empty means no extra tags.
	BuildTags []string

	// Dir is the directory to run packages.Load from. Empty means the
	// current working directory.
	Dir string
}

// Load builds a Graph by running packages.Load against patterns. The
// module path is auto-detected from the first loaded package's Module
// field. Callers do not pass a module path.
func Load(opts LoadOptions, patterns ...string) (*Graph, error) {
	cfg := &packages.Config{
		Mode:  loadMode,
		Tests: opts.IncludeTests,
		Dir:   opts.Dir,
	}
	if len(opts.BuildTags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(opts.BuildTags, ",")}
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("packages.Load: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages loaded for patterns %v", patterns)
	}
	// packages.Load reports per-package failures (malformed import path,
	// type-check failure, missing source) on Package.Errors instead of the
	// top-level err. A graph built from a partial load would silently miss
	// nodes / edges; downstream T-rule closures would then return false
	// negatives. Surface the first error and let the caller decide.
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("packages.Load: package %q: %v", p.PkgPath, p.Errors[0])
		}
	}
	module := detectModule(pkgs)
	if module == "" {
		return nil, errors.New("module path not detected; load with NeedModule and ensure go.mod exists")
	}
	return FromPackages(module, pkgs), nil
}

// FromPackages builds a Graph from already-loaded packages. module must be
// the bare module path (e.g. "github.com/ghbvf/gocell"). It is the
// injection point for callers that share a packages.Load with another
// consumer; archtest uses this to reuse typeseval.SharedResolver's cached
// load instead of running packages.Load twice per test run.
//
// Only structural fields (PkgPath, Imports, ID for test-variant filtering)
// are read; pkgs is not retained after the call returns.
func FromPackages(module string, pkgs []*packages.Package) *Graph {
	g := &Graph{
		Module: module,
		byID:   make(map[string]*Node, len(pkgs)),
	}
	for _, p := range pkgs {
		if p == nil || p.PkgPath == "" {
			continue
		}
		// Skip synthetic test variants (`<pkg>.test` binary, bracketed
		// `<pkg> [<pkg>.test]` internal-test compile). They are walked
		// for TestOnly detection in markTestOnly but do not appear as
		// graph nodes.
		if isTestVariant(p.ID) {
			continue
		}
		if _, dup := g.byID[p.PkgPath]; dup {
			continue
		}
		n := &Node{
			ID:      p.PkgPath,
			Layer:   LayerOf(module, p.PkgPath),
			CellID:  CellOf(module, p.PkgPath),
			SliceID: SliceOf(module, p.PkgPath),
		}
		n.Imports = make([]string, 0, len(p.Imports))
		for imp := range p.Imports {
			n.Imports = append(n.Imports, imp)
		}
		sort.Strings(n.Imports)
		g.Packages = append(g.Packages, n)
		g.byID[p.PkgPath] = n
	}
	sort.Slice(g.Packages, func(i, j int) bool { return g.Packages[i].ID < g.Packages[j].ID })
	g.markTestOnly(pkgs)
	g.Stats.Packages = len(g.Packages)
	edges := 0
	for _, n := range g.Packages {
		edges += len(n.Imports)
	}
	g.Stats.Edges = edges
	return g
}

// detectModule returns the first non-empty Module.Path from pkgs, or "".
func detectModule(pkgs []*packages.Package) string {
	for _, p := range pkgs {
		if p != nil && p.Module != nil && p.Module.Path != "" {
			return p.Module.Path
		}
	}
	return ""
}

// markTestOnly tags each node TestOnly=true when at least one test
// variant imports it AND no production package imports it. This isolates
// helper packages whose sole consumers are *_test.go files. Leaf or
// orphaned packages (no importers at all) stay TestOnly=false because
// they are not test-specific — they may be entry points or unused
// production code.
//
// A test variant has an ID containing ".test]" or ending in ".test".
// When IncludeTests is false, no test variants are loaded and no node is
// marked TestOnly.
func (g *Graph) markTestOnly(pkgs []*packages.Package) {
	prodImports, testImports := collectImporters(pkgs)
	for _, n := range g.Packages {
		if !prodImports[n.ID] && testImports[n.ID] {
			n.TestOnly = true
		}
	}
}

// collectImporters partitions all import edges in pkgs into production-side
// and test-side sets, keyed by importee. The `<pkg>.test` synthetic binary
// trivially imports `<pkg>`; that structural edge is filtered out so the
// package under test is not mis-marked as test-only.
func collectImporters(pkgs []*packages.Package) (prod, test map[string]bool) {
	prod = make(map[string]bool, len(pkgs))
	test = make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		if p == nil {
			continue
		}
		if isTestVariant(p.ID) {
			// TrimSuffix relies on the golang.org/x/tools/go/packages convention
			// that a test-binary ID is exactly "<PkgPath>.test" when Tests=true.
			// If TrimSuffix has no effect (selfTested == p.ID), the equality guard
			// below never fires — harmless, because isTestVariant already confirmed
			// a ".test]" or ".test" suffix is present.
			selfTested := strings.TrimSuffix(p.ID, ".test")
			for imp := range p.Imports {
				if imp == selfTested {
					continue
				}
				test[imp] = true
			}
			continue
		}
		for imp := range p.Imports {
			prod[imp] = true
		}
	}
	return prod, test
}

// isTestVariant reports whether a package ID is a test variant produced by
// packages.Load with Tests=true.
func isTestVariant(id string) bool {
	return strings.Contains(id, ".test]") || strings.HasSuffix(id, ".test")
}
