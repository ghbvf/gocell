package depgraph

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/tools/packagesload"
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

// Load builds a Graph by running packages.Load against patterns in single-module
// mode (GOWORK=off): it loads one standalone module (the testdata/synth fixture,
// or any caller-supplied Dir not in the repo go.work `use` set). The module set
// is auto-detected from the loaded packages' Module fields. For a workspace-wide
// graph spanning every go.work member, use [LoadWorkspace].
func Load(opts LoadOptions, patterns ...string) (*kerneldepgraph.Graph, error) {
	return loadWithMode(packagesload.ModeModule, opts, patterns...)
}

// LoadWorkspace builds a Graph spanning every module matched by patterns under
// the ambient go.work workspace (GOWORK on). Callers pass one `<importPath>/...`
// pattern per workspace member (see tools/workspace.Modules); the module set is
// auto-detected from the loaded packages' Module fields, so nested modules are
// classified within their own module (LayerCells, not LayerUnknown) rather than
// as unknown directories of the core module.
func LoadWorkspace(opts LoadOptions, patterns ...string) (*kerneldepgraph.Graph, error) {
	return loadWithMode(packagesload.ModeWorkspace, opts, patterns...)
}

// loadWithMode is the shared body of Load / LoadWorkspace; mode selects the
// GOWORK semantics (see tools/packagesload).
func loadWithMode(mode packagesload.Mode, opts LoadOptions, patterns ...string) (*kerneldepgraph.Graph, error) {
	cfg := &packages.Config{
		Mode:  loadMode,
		Tests: opts.IncludeTests,
		Dir:   opts.Dir,
	}
	if len(opts.BuildTags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(opts.BuildTags, ",")}
	}
	pkgs, err := packagesload.Load(mode, cfg, patterns...)
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
			return nil, fmt.Errorf("packages.Load: package %q: %w", p.PkgPath, p.Errors[0])
		}
	}
	modules := detectModules(pkgs)
	if len(modules) == 0 {
		return nil, errors.New("module path(s) not detected; load with NeedModule and ensure go.mod exists")
	}
	return FromPackages(modules, pkgs), nil
}

// FromPackages builds a Graph from already-loaded packages classified against
// the given module set. modules is the set of module import paths the graph
// spans (one element for a single-module load; every workspace member for a
// multi-module load). It is the injection point for callers that share a
// packages.Load with another consumer; archtest uses this to reuse
// typeseval.SharedResolver's cached load instead of running packages.Load twice
// per test run — passing the workspace module set from tools/workspace.Modules
// (its own load omits NeedModule, so it cannot detect the set from packages).
//
// Only structural fields (PkgPath, Imports, ID for test-variant filtering)
// are read; pkgs is not retained after the call returns.
func FromPackages(modules []string, pkgs []*packages.Package) *kerneldepgraph.Graph {
	cls := kerneldepgraph.NewClassifier(modules)
	nodes := make([]*kerneldepgraph.Node, 0, len(pkgs))
	for _, p := range pkgs {
		if p == nil || p.PkgPath == "" {
			continue
		}
		// Skip synthetic test variants (`<pkg>.test` binary, bracketed
		// `<pkg> [<pkg>.test]` internal-test compile). They are walked
		// for TestOnly detection in MarkTestOnly but do not appear as
		// graph nodes.
		if isTestVariant(p.ID) {
			continue
		}
		n := &kerneldepgraph.Node{
			ID:      p.PkgPath,
			Layer:   cls.Layer(p.PkgPath),
			CellID:  cls.Cell(p.PkgPath),
			SliceID: cls.Slice(p.PkgPath),
		}
		n.Imports = make([]string, 0, len(p.Imports))
		for imp := range p.Imports {
			n.Imports = append(n.Imports, imp)
		}
		sort.Strings(n.Imports)
		nodes = append(nodes, n)
	}
	g := kerneldepgraph.FromNodes(modules, nodes)
	prod, test := collectImporters(pkgs)
	kerneldepgraph.MarkTestOnly(g, prod, test)
	return g
}

// detectModules returns the distinct non-empty Module.Path values across pkgs,
// sorted for determinism. Because the load patterns match only workspace
// members (never their third-party dependencies), this yields exactly the
// workspace module set.
func detectModules(pkgs []*packages.Package) []string {
	seen := make(map[string]struct{})
	var mods []string
	for _, p := range pkgs {
		if p == nil || p.Module == nil || p.Module.Path == "" {
			continue
		}
		if _, dup := seen[p.Module.Path]; dup {
			continue
		}
		seen[p.Module.Path] = struct{}{}
		mods = append(mods, p.Module.Path)
	}
	sort.Strings(mods)
	return mods
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
