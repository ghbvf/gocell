package app

// package_graph.go — the single workspace-aware package-dependency-graph loader
// shared by `gocell graph`, `gocell generate catalog`, and `gocell export
// catalog`. Centralizing it here means every CLI entry point derives its scan
// set from go.work the same way: a module extracted into go.work is graphed by
// all three, not silently dropped by some (#1555 review C1/C2).

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/tools/depgraph"
	"github.com/ghbvf/gocell/tools/workspace"
)

// loadPackageGraph builds the package dependency graph rooted at root. When root
// is a workspace (a go.work is present) it spans EVERY workspace member module
// via ModeWorkspace + one relative-dir "./<dir>/..." pattern per member, so a
// nested module extracted into go.work is graphed rather than silently dropped.
// When root has no go.work it loads that single standalone module via "./..."
// (GOWORK=off).
//
// The mode is selected by go.work presence — an explicit branch, not a silent
// default — so both the in-repo workspace graph and an arbitrary standalone
// module remain expressible. This is the single source the graph / generate /
// export commands share; previously generate + export used a single-module
// depgraph.Load("./...") that dropped satellite modules under go.work.
func loadPackageGraph(root string, includeTests bool) (*kerneldepgraph.Graph, error) {
	lo := depgraph.LoadOptions{IncludeTests: includeTests, Dir: root}
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		// No go.work above the root → single standalone module.
		return depgraph.Load(lo, defaultGraphPattern)
	}
	patterns, err := workspacePatterns(root)
	if err != nil {
		return nil, err
	}
	return depgraph.LoadWorkspace(lo, patterns...)
}

// workspacePatterns enumerates the go.work member modules rooted at root and
// returns one RELATIVE-DIR "./<dir>/..." pattern per member.
//
// Relative-dir patterns (not "<importPath>/..." module-path patterns) are
// required under go.work: a module-path pattern makes `go` resolve a workspace
// member as an external dependency at its required version (a network fetch /
// "unrecognized import path" failure), whereas a directory pattern resolves the
// local member. This mirrors typeseval.LoadProductionPackages — the keystone
// that keeps the scan set derived from go.work rather than a hand-maintained
// list. For a single-module workspace (today's `use .`, Dir ".") the pattern
// reduces to "./..." — byte-identical to the former single-module load.
func workspacePatterns(root string) ([]string, error) {
	mods, err := workspace.Modules(root)
	if err != nil {
		return nil, fmt.Errorf("enumerate workspace modules: %w", err)
	}
	patterns := make([]string, len(mods))
	for i, m := range mods {
		patterns[i] = "./" + path.Join(filepath.ToSlash(m.Dir), "...")
	}
	return patterns, nil
}
