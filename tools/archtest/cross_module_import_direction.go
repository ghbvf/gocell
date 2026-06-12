package archtest

import (
	"sort"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
)

// cross_module_import_direction.go — the CROSS-MODULE-IMPORT-DIRECTION-01 rule
// body, kept in a non-test .go file so the workspace-root model (#1555) can be
// reused without copying the rule logic (mirrors external.go's Check* funcs).
//
// The rule is workspace-specific (it reasons about the relationship BETWEEN
// workspace member modules), so it is NOT a portable StandardCellRule — an
// external single-module repo has no "other" workspace module. It operates on a
// *kerneldepgraph.Graph that spans the whole workspace.

// CrossModuleViolation is one base→satellite import edge: a package owned by the
// base (core) module importing a package owned by a different workspace member.
// Such an edge inverts the workspace dependency direction (Plan D: "顶层 = core";
// satellites depend on core, never the reverse).
type CrossModuleViolation struct {
	// Pkg is the base-module package performing the import.
	Pkg string
	// Import is the imported package owned by a satellite module.
	Import string
	// Module is the satellite module that owns Import.
	Module string
}

// CheckCrossModuleImportDirection enforces CROSS-MODULE-IMPORT-DIRECTION-01: no
// package owned by coreModule may import a package owned by a DIFFERENT workspace
// member module. The base (core) module is the workspace root module (go.work
// `use .` — github.com/ghbvf/gocell for the platform); satellites
// (github.com/ghbvf/gocell/mdm, /zerotrust, separate-module examples) may import
// core, but core must never import a satellite, or extracting a satellite would
// drag the whole base with it.
//
// The live workspace already holds satellite modules (adapters/*, corecells,
// tools, generated, …), so this is an active gate: it reports no violation only
// because no core-module package imports a satellite (correct layering), not
// because the workspace is single-module. The synthetic fixture exercises the
// firing path.
//
// AI-robust grade: Medium (archtest type-aware via g.Modules + the
// kerneldepgraph.Classifier longest-prefix owner resolution on each import edge;
// import-path strings come from the typed packages.Load, not a string anchor).
// A Hard form for the GENERIC "core ⊀ any satellite" shape is unreachable — Go's
// type system / package visibility cannot express "a package in module A must not
// import any package in module B" (same permanent ceiling as #851 / #893 /
// #1282). The Hard COMPLEMENT lands per concrete satellite at extraction time: a
// `.golangci.yml` depguard rule banning the satellite's import path from core
// packages is a path-level compile-fast gate. This generic archtest auto-covers
// every future satellite (including ones not yet enumerated in depguard);
// per-satellite depguard Hard-ization is tracked as the close-out task (gh #1590).
//
// Blind spots (see cross_module_import_direction_test.go for the reverse
// self-checks): a base package reaching a satellite via a string-built import
// path (not a real edge) is not modeled — depgraph edges come from real
// packages.Load Imports, so a non-importing reference cannot create an edge.
func CheckCrossModuleImportDirection(g *kerneldepgraph.Graph, coreModule string) []CrossModuleViolation {
	if g == nil || coreModule == "" {
		return nil
	}
	cls := kerneldepgraph.NewClassifier(g.Modules)
	var out []CrossModuleViolation
	for _, n := range g.Packages {
		if n == nil {
			continue
		}
		if cls.OwningModule(n.ID) != coreModule {
			continue
		}
		for _, imp := range n.Imports {
			owner := cls.OwningModule(imp)
			if owner != "" && owner != coreModule {
				out = append(out, CrossModuleViolation{Pkg: n.ID, Import: imp, Module: owner})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pkg != out[j].Pkg {
			return out[i].Pkg < out[j].Pkg
		}
		return out[i].Import < out[j].Import
	})
	return out
}
