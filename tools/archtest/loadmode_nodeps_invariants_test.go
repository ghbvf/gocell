package archtest

// INVARIANT: ARCHTEST-LOADMODE-DROP-NEEDDEPS-01
//
// Non-vacuity guards for the transitive (*types.Package).Imports() walks after
// dropping packages.NeedDeps from the typeseval load mode (#1499). When NeedDeps
// is absent, go/packages builds dependency *types.Package values from export
// data and prunes the transitive import closure to type-referenced packages.
// Each archtest rule below walks that closure to resolve a target package/type;
// these tests assert the walks STILL resolve their real targets under the pruned
// mode, so the optimization cannot silently make a rule vacuous (resolve nothing
// → find nothing → pass trivially).
//
// Soundness: for the NAMED-TYPE detection paths a finding requires the scanned
// root to type-reference the target's symbol, keeping the target a DIRECT import
// that survives pruning — so those paths are sound under pruning by construction.
//
// The one path not covered by that argument is loadForbiddenIfacesFromPkg's
// types.Implements structural-match (an anonymous/local interface matching a
// forbidden method set need NOT name the forbidden package). Its per-Pass coverage
// is left as a DOCUMENTED BOUNDARY rather than a permanent machine guard, for two
// reasons established while #1499 landed:
//
//   1. A permanent per-Pass proof is a WithDeps-vs-NoDeps differential, i.e. a
//      second whole-module-ish packages.Load — exactly the cost #1499 removes. The
//      codebase rejects that three independent ways: the ARCHTEST-PASS-FUNNEL
//      Hard-line depguard (ADR 202605141519 defense #2) bans a second packages.Load
//      in archtest *_test.go; the 20s slowgate rejects the load's cold-CI latency;
//      and typeseval's TestKnownNonDefaultTagsCoverage rejects a `//go:build !race`
//      escape hatch. The architecture deliberately forbids expensive whole-module
//      loads in the suite.
//   2. The differential is also semantically corpus-contingent, not an invariant:
//      WithDeps resolves by import-reachability, NoDeps by type-reference; the
//      former is a superset, so "NoDeps ⊇ WithDeps" holds only because every
//      current cell that import-reaches a forbidden package also type-references it.
//      A legitimate import-only dependency chain would make such a differential
//      FALSE-fail on correct code.
//
// Coverage actually in force: (a) the named-type soundness theorem above; (b) the
// funnel smokes below, asserting the REAL rule helpers stay non-vacuous under the
// production NoDeps mode; (c) the one-time full NoDeps-vs-WithDeps comparison run
// when #1499 landed (the whole archtest suite's failure set was byte-identical
// across both modes). The residual structural-match anonymous-interface case is
// empty on the real corpus and fundamentally differential — a deliberately accepted
// boundary, not a defect.
//
// Covered walks:
//   - loadForbiddenIfacesFromPkg              (cell_public_option_param_test.go, BFS)
//   - resolveSagaStepFuncType → findImportedPkg     (saga_invariants_test.go, DFS)
//   - lookupInterface → findImportedPackage   (aftercommit_pure_transient_invariants.go, DFS)
//   - findTypesPackageByPath                  (governance_rules_invariants_test.go, DFS)
//     — its transitive walk is exercised by the production rule callsite
//     (governance_rules_invariants_test.go:550, a fixture root that imports
//     kernel/governance as a DEPENDENCY) running under this same mode; that rule
//     fails loudly if the walk goes vacuous. Note: the existing
//     TestFindTypesPackageByPath/known_path_found only covers the self-path case
//     (root.Path()==target → matched at depth 0, imports never walked), so it
//     proves no-panic, NOT transitive resolution. Not duplicated here.
//
// Rating: Medium (type-aware; binds resolution to a real *types.Package). Hard
// is unreachable — dependency-graph pruning is go/packages runtime behavior, not
// type-system-sealable. Companion mode guard: typeseval.TestLoadMode_NoNeedDeps.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLoadModeNoDeps_NonVacuity_CellForbiddenIfaces is the funnel-compatible
// non-vacuity smoke for the cell raw-option rule: under the production NoDeps mode,
// the ACTUAL loadForbiddenIfacesFromPkg (not a reimplementation) resolves every
// forbidden canonical somewhere across the cell+example corpus the rule scans. This
// is the permanent guard the architecture permits cheaply; the per-Pass
// structural-match boundary (why a permanent WithDeps differential is intentionally
// not added) is documented in this file's package doc above.
func TestLoadModeNoDeps_NonVacuity_CellForbiddenIfaces(t *testing.T) {
	resolved := map[string]bool{}
	var cellPasses int
	Run(t, WorkspaceTyped(TypedOpts{Tests: false}, []string{"./corecells/...", "./examples/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || !isCellSubtreePkgPath(p.Pkg.Path()) {
			return nil
		}
		cellPasses++
		for canonical := range loadForbiddenIfacesFromPkg(p.Pkg) {
			resolved[canonical] = true
		}
		return nil
	})

	require.Positive(t, cellPasses, "must scan >=1 cell-subtree Pass under ./corecells/... + ./examples/...")
	for canonical := range rawPublicOptionForbidden {
		require.Containsf(t, resolved, canonical,
			"forbidden iface %q resolved in NO cell-subtree Pass under NoDeps via the real "+
				"loadForbiddenIfacesFromPkg — deep-detection globally vacuous", canonical)
	}
}

// isCellSubtreePkgPath reports whether an import path lies in a cell subtree:
// platform corecells/<id>/… or example examples/<demo>/cells/<id>/….
func isCellSubtreePkgPath(pkgPath string) bool {
	return strings.Contains(pkgPath, "/cells/") ||
		strings.Contains(pkgPath, "/corecells/")
}

// TestLoadModeNoDeps_NonVacuity_SagaStepFunc asserts resolveSagaStepFuncType still
// resolves kernel/saga.StepFunc from a runtime/saga package under the NoDeps mode
// (the SAGA-STEP-RUN-OUTSIDE-TX A1 callsite-uniqueness gate would silently no-op
// if this returned nil).
func TestLoadModeNoDeps_NonVacuity_SagaStepFunc(t *testing.T) {
	var found bool
	Run(t, WorkspaceTyped(TypedOpts{}, []string{"./runtime/saga/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		if resolveSagaStepFuncType(p.Pkg) != nil {
			found = true
		}
		return nil
	})
	require.True(t, found,
		"resolveSagaStepFuncType must resolve kernel/saga.StepFunc from at least one runtime/saga package under the NoDeps load mode")
}

// TestLoadModeNoDeps_NonVacuity_AfterCommitBannedReceivers asserts lookupInterface
// still resolves both transitively-walked banned receiver interfaces (pgx.Tx and
// kernel/outbox.Writer) under the NoDeps mode. database/sql.Tx is matched by
// exact name (not via the transitive walk) and is unaffected.
func TestLoadModeNoDeps_NonVacuity_AfterCommitBannedReceivers(t *testing.T) {
	const pgxPath = "github.com/jackc/pgx/v5"
	outboxPath := PlatformModulePath + "/kernel/outbox"

	var pgxFound, outboxWriterFound bool
	Run(t, WorkspaceTyped(TypedOpts{}, []string{"./adapters/postgres/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		if lookupInterface(p.Pkg, pgxPath, "Tx") != nil {
			pgxFound = true
		}
		if lookupInterface(p.Pkg, outboxPath, "Writer") != nil {
			outboxWriterFound = true
		}
		return nil
	})
	require.True(t, pgxFound, "lookupInterface must resolve pgx.Tx under the NoDeps load mode")
	require.True(t, outboxWriterFound, "lookupInterface must resolve outbox.Writer under the NoDeps load mode")
}
