// INVARIANT: AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01
//
// AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01: ledger.NewProtocol /
// ledger.MustNewProtocol may only be invoked from cmd/* (composition root)
// or the enumerated allowlist {runtime/audit/ledger, runtime/audit/ledger/storetest}.
// Cells, runtime/* (non-ledger), adapters/*, and tests outside ledger/* must
// receive an injected *ledger.Protocol — not construct one.
//
// AI-rebust 评级：Medium-true (type-aware via typeseval.LoadProductionPackages
// + typeseval.ResolvePackageRef). Resolution is by canonical *types.PkgName
// import path (info.Uses[sel.X].(*types.PkgName).Imported().Path()), NOT AST
// identifier name, so import aliases (`import foo "runtime/audit/ledger";
// foo.MustNewProtocol(...)`) cannot bypass detection. Closes K-01 + A-10 from
// PR #450 review which flagged the prior AST-only pkg.Name == "ledger"
// matcher as silently Soft.
//
// Hard is unattainable for this rule shape — cells must import ledger to
// consume the typed `*ledger.Protocol` shape (ledger.Entry, ledger.Store
// interface), so banning the import wholesale would defeat the typed-Go
// paradigm. Adopting a composition-root token (akin to `WrapForCell`) would
// require restructuring ledger.NewProtocol's signature and is out of scope
// for an archtest upgrade PR. The type-aware archtest is the highest grade
// reachable here; see ai-collab.md §"载体决策原则" ≥ Medium 立项硬门槛.
//
// Allowlist is enumerated, not prefix-based — adding a sibling sub-package
// (e.g. `runtime/audit/ledger/dump/`) must be an explicit decision (K-04).
//
// Sentinel sticky doctrine: 4 wiring options (WithChainHMAC / WithNamespace /
// WithRestartRecovery / WithIdempotency) each have a xxxNil bool sticky flag
// that is set when a nil interface value is received and is never cleared by
// a subsequent valid call — misconfiguration must not be silently masked.
package archtest

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// ledgerImportSuffix is the module-relative path of the ledger package.
// Combined with modulePath (read from go.mod) it forms the canonical import
// path matched by ResolvePackageRef.
const ledgerImportSuffix = "/runtime/audit/ledger"

var ledgerForbidden = map[string]bool{
	"NewProtocol":     true,
	"MustNewProtocol": true,
}

// ledgerCompositionRootAllowlist enumerates package paths exempt from the
// rule. Sub-directories under `runtime/audit/ledger/` are NOT automatically
// included — each must be listed explicitly (K-04). Stored as suffixes
// (module-relative paths starting with "/") so the test does not depend on
// a hardcoded module name.
var ledgerCompositionRootAllowlist = map[string]bool{
	"/runtime/audit/ledger":           true,
	"/runtime/audit/ledger/storetest": true,
}

type ledgerHit struct {
	file string
	line int
	name string
}

// scanLedgerCompositionRootViolations walks call expressions and records
// invocations of forbidden ledger constructors outside the allowlist.
//
// When restrictScopeDirs is true (real-repo invariant), only packages whose
// module-relative path starts with /cells/, /runtime/, or /adapters/ are
// scanned; cmd/* and examples/* are exempted because they own their own
// composition roots and are the legitimate construction sites. When false
// (fixture detection test), all supplied packages are scanned so a fixture
// living under tools/archtest/internal/ still produces hits.
//
// The bool flag mirrors the precedent shape used by
// scanPackagesForRawPublicOption in cell_public_option_param_test.go — both
// gate "real-repo path filtering" off when called from a fixture-scoped
// SharedResolver load whose `pkgs` argument is itself narrowed to the
// fixture package, so the false branch is safe only when the caller has
// already restricted the package set.
func scanLedgerCompositionRootViolations(root, modulePath string, pkgs []*packages.Package, restrictScopeDirs bool) []ledgerHit {
	var hits []ledgerHit
	ledgerImportPath := modulePath + ledgerImportSuffix

	for _, pkg := range pkgs {
		if pkg == nil || pkg.TypesInfo == nil {
			continue
		}
		pkgSuffix := strings.TrimPrefix(pkg.PkgPath, modulePath)
		if ledgerCompositionRootAllowlist[pkgSuffix] {
			continue
		}
		if restrictScopeDirs {
			if strings.HasPrefix(pkgSuffix, "/cmd/") || strings.HasPrefix(pkgSuffix, "/examples/") {
				continue
			}
			if !strings.HasPrefix(pkgSuffix, "/cells/") &&
				!strings.HasPrefix(pkgSuffix, "/runtime/") &&
				!strings.HasPrefix(pkgSuffix, "/adapters/") {
				continue
			}
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			if strings.HasSuffix(absPath, "_test.go") {
				continue
			}
			rel, err := filepath.Rel(root, absPath)
			if err != nil {
				continue
			}
			relSlash := filepath.ToSlash(rel)
			scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				pkgPath, name, ok := typeseval.ResolvePackageRef(pkg.TypesInfo, call.Fun)
				if !ok || pkgPath != ledgerImportPath {
					return
				}
				if !ledgerForbidden[name] {
					return
				}
				hits = append(hits, ledgerHit{
					file: relSlash,
					line: pkg.Fset.Position(call.Pos()).Line,
					name: name,
				})
			})
		}
	}
	return hits
}

func TestAuditLedgerProtocol_CompositionRootOnly(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)

	resolver, err := typeseval.LoadProductionPackages(root, modulePath, false, nil)
	require.NoError(t, err)

	hits := scanLedgerCompositionRootViolations(root, modulePath, resolver.Production(), true)

	for _, h := range hits {
		t.Logf("AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01: %s:%d calls ledger.%s "+
			"outside cmd/ + enumerated allowlist {runtime/audit/ledger, runtime/audit/ledger/storetest}",
			h.file, h.line, h.name)
	}
	assert.Empty(t, hits,
		"AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01: ledger.NewProtocol / ledger.MustNewProtocol "+
			"must only be called from cmd/* (composition root) or the enumerated allowlist; "+
			"cells/runtime/adapters must consume an injected *ledger.Protocol")
}

// TestAuditLedgerProtocol_ScannerCatchesAliasBypass loads the build-tag-gated
// auditledgerfixture package and asserts the scanner reports the aliased
// import call site that the prior AST-only `pkg.Name == "ledger"` matcher
// silently passed (K-01 / A-10 bypass surface).
//
// ResolvePackageRef resolves info.Uses[sel.X] → *types.PkgName →
// Imported().Path(), so the canonical import path matches regardless of the
// alias chosen at import.
//
// Per ai-collab.md §"Hard 范本": fixture is a real Go package loaded via
// packages.Load with the archtest_fixture build tag. Bypassing this test
// requires modifying real source code.
func TestAuditLedgerProtocol_ScannerCatchesAliasBypass(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)
	resolver, err := typeseval.SharedResolver(root, false, []string{"archtest_fixture"},
		"./tools/archtest/internal/auditledgerfixture")
	require.NoError(t, err)

	hits := scanLedgerCompositionRootViolations(root, modulePath, resolver.Packages(), false)

	require.Len(t, hits, 1,
		"fixture must yield exactly 1 violation: AliasedMustNewProtocol uses "+
			"`import auditledger \"<module>/runtime/audit/ledger\"; auditledger.MustNewProtocol(nil)`; "+
			"the prior AST-only pkg.Name == \"ledger\" matcher would silently pass this")

	got := hits[0]
	assert.Equal(t, "MustNewProtocol", got.name)
	assert.Contains(t, got.file, "tools/archtest/internal/auditledgerfixture/",
		"fixture hit must be located in the auditledgerfixture package directory")
}
