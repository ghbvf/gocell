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
// that survives pruning. The one exception is loadForbiddenIfacesFromPkg's
// types.Implements structural-match path — an anonymous/local interface whose
// method set matches a forbidden interface need NOT name the forbidden package,
// so the resolved forbidden-interface set depends on the cell's import closure
// reaching that package. That holds for every current platform cell (all directly
// import kernel/persistence + kernel/outbox), which the CellForbiddenIfaces guard
// below machine-asserts across ./cells/... (len == len(rawPublicOptionForbidden)).
// These guards lock that property machine-side, replacing the issue's one-shot
// "NoDeps vs WithDeps" manual comparison with a durable regression check.
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

// TestLoadModeNoDeps_NonVacuity_CellForbiddenIfaces asserts loadForbiddenIfacesFromPkg
// resolves each rawPublicOptionForbidden canonical (kernel/persistence + kernel/outbox
// interfaces) under the NoDeps load mode at the SAME granularity the real rule
// (scanPassForRawPublicOption) runs — per cell-subtree Pass, not one corpus-wide union.
// A union dominated by a single cell (e.g. accesscore) could pass even if NoDeps pruning
// dropped a forbidden package from another cell's Pass closure; requiring each iface to
// resolve in >=2 INDEPENDENT cells (across ./cells/... + ./examples/...) closes that gap.
// This guards the types.Implements structural-match deep-detection path, which depends on
// the resolved *types.Interface values (not the canonical-name fall-through) and is the
// corpus property the loadMode soundness note relies on.
func TestLoadModeNoDeps_NonVacuity_CellForbiddenIfaces(t *testing.T) {
	// resolvers[canonical] = set of distinct cell IDs whose Pass resolved it.
	resolvers := make(map[string]map[string]bool, len(rawPublicOptionForbidden))
	for c := range rawPublicOptionForbidden {
		resolvers[c] = map[string]bool{}
	}
	var cellPasses int
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./cells/...", "./examples/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		cellID, ok := cellIDFromPkgPath(p.Pkg.Path())
		if !ok {
			return nil // not a cell-subtree package — outside the rule's scan scope
		}
		cellPasses++
		for canonical := range loadForbiddenIfacesFromPkg(p.Pkg) {
			resolvers[canonical][cellID] = true
		}
		return nil
	})

	require.Positive(t, cellPasses, "must scan >=1 cell-subtree Pass under ./cells/... + ./examples/...")
	for canonical := range rawPublicOptionForbidden {
		require.GreaterOrEqualf(t, len(resolvers[canonical]), 2,
			"forbidden iface %q resolved in only %d cell(s) %v under NoDeps; require >=2 independent cells "+
				"(per-Pass non-vacuity — a corpus union could pass on one cell alone while pruning dropped "+
				"the package from another cell's Pass closure)", canonical, len(resolvers[canonical]), resolvers[canonical])
	}
}

// cellIDFromPkgPath extracts the cell ID from an import path containing a
// ".../cells/<cellID>" segment — both platform cells/<id>/… and example
// examples/<demo>/cells/<id>/… — matching the rule's cell-subtree scan scope.
func cellIDFromPkgPath(pkgPath string) (string, bool) {
	const marker = "/cells/"
	i := strings.Index(pkgPath, marker)
	if i < 0 {
		return "", false
	}
	rest := pkgPath[i+len(marker):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return rest, rest != ""
}

// TestLoadModeNoDeps_NonVacuity_SagaStepFunc asserts resolveSagaStepFuncType still
// resolves kernel/saga.StepFunc from a runtime/saga package under the NoDeps mode
// (the SAGA-STEP-RUN-OUTSIDE-TX A1 callsite-uniqueness gate would silently no-op
// if this returned nil).
func TestLoadModeNoDeps_NonVacuity_SagaStepFunc(t *testing.T) {
	var found bool
	Run(t, Typed(TypedOpts{}, []string{"./runtime/saga/..."}), func(p *Pass) []Diagnostic {
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
	Run(t, Typed(TypedOpts{}, []string{"./adapters/postgres/..."}), func(p *Pass) []Diagnostic {
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
