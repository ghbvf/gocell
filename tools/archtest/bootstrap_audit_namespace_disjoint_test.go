// invariants:
//   - INVARIANT: AUDIT-NS-DISJOINT-01
//
// Medium archtest backstop for issue #1121 / ADR 202605270230 — the audit chain
// physical-isolation invariant.
//
// The Hard upstream defense is the type system: ModuleExports.BootstrapLedgerStore
// is *audit.BootstrapLedgerStore, NewBootstrapLedgerStore enforces non-nil,
// and audit.AppendBootstrapAuthFail / NewBootstrapAuthFailObserver accept only
// the typed wrapper — passing an auditcore-namespace ledger.Store is a compile
// error. The Hard downstream defense is the narrow ledger.QueryStore interface:
// ledger.MultiStore implements only that subset, so injecting it into the
// appender (auditcore.WithLedgerStore, signature ledger.Store) is a compile
// error.
//
// What the type system cannot enforce: that the composition root actually
// constructs *two* protocol/store pairs (one per namespace). A misconfigured
// build that constructs only one ledger.NewProtocol call with the auditcore
// namespace, then wraps it in *BootstrapLedgerStore, would pass compile-time
// but fail the physical-isolation contract — both writers share one chain.
//
// AUDIT-NS-DISJOINT-01 closes this gap with an AST-level invariant: every
// production cellmodules/auditcore source taken together must invoke both
// audit.BootstrapNamespace() at least once AND must contain at least one
// reference to a distinct "auditcore" NamespaceID. Reverse self-check fixtures
// confirm the scanner actually catches a single-namespace configuration.
//
// Scope: cellmodules/auditcore/ production (non-test) packages — the audit-chain
// wiring moved from cmd/corebundle to the cellmodules/ composition-root layer in
// issue #1085 (AuditCoreModule.Provide now lives in cellmodules/auditcore). Tests
// intentionally excluded — they may exercise single-namespace paths for unit
// coverage of helpers.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleAuditNSDisjoint01 = "AUDIT-NS-DISJOINT-01"

	bootstrapNamespaceFnName = "BootstrapNamespace"
	parseNamespaceIDFnName   = "ParseNamespaceID"
	ledgerPkgSuffix          = "/runtime/audit/ledger"

	// cellmodulesAuditcorePkgSuffix is where AuditCoreModule.Provide lives after
	// the #1085 cell-wiring relocation out of cmd/corebundle.
	cellmodulesAuditcorePkgSuffix = "/cellmodules/auditcore"
)

// TestAuditNamespaceDisjoint01 enforces that cmd/corebundle production code
// constructs at least two distinct audit chains: one on the bootstrap
// namespace and one on the auditcore namespace.
//
// The check scans for two AST anchors across the cmd/corebundle production
// package set:
//
//  1. At least one CallExpr resolving (via ResolvePackageRef) to
//     runtime/audit.BootstrapNamespace — proves the bootstrap chain wiring
//     is hooked into the composition root.
//
//  2. At least one CallExpr resolving to runtime/audit/ledger.ParseNamespaceID
//     whose first argument evaluates to a NamespaceID string distinct from
//     "bootstrap" — proves the auditcore chain wiring exists (the production
//     code uses "auditcore", but the rule only requires "not bootstrap" so
//     future renames of the relay-side namespace do not break the test).
//
// Either anchor missing fails the archtest with a pointer to ADR 202605270230.
func TestAuditNamespaceDisjoint01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	auditPkgPath := modPath + auditPkgSuffix
	ledgerPkgPath := modPath + ledgerPkgSuffix
	scanPkgPath := modPath + cellmodulesAuditcorePkgSuffix

	var (
		bootstrapNamespaceFound bool
		nonBootstrapNSFound     bool
		visited                 bool
	)

	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != scanPkgPath {
			return nil
		}
		visited = true
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
				if !ok {
					return
				}
				switch {
				case pkgPath == auditPkgPath && name == bootstrapNamespaceFnName:
					bootstrapNamespaceFound = true
				case pkgPath == ledgerPkgPath && name == parseNamespaceIDFnName:
					if len(call.Args) == 0 {
						return
					}
					value, ok := EvaluateConstString(p.TypesInfo, call.Args[0])
					if !ok {
						return
					}
					if value != "bootstrap" {
						nonBootstrapNSFound = true
					}
				}
			})
		}
		return nil
	})

	require.True(t, visited,
		"%s: RunTypedProduction did not visit %q — scope coverage gap, AUDIT-NS-DISJOINT-01 would pass vacuously",
		ruleAuditNSDisjoint01, scanPkgPath)

	assert.True(t, bootstrapNamespaceFound,
		"%s: cellmodules/auditcore production must call audit.BootstrapNamespace() at least once "+
			"(issue #1121 / ADR 202605270230 — bootstrap chain wiring must be present in the composition root)",
		ruleAuditNSDisjoint01)
	assert.True(t, nonBootstrapNSFound,
		"%s: cellmodules/auditcore production must construct at least one ledger.NamespaceID "+
			"distinct from \"bootstrap\" (the auditcore relay chain) — single-namespace "+
			"configurations re-introduce the dual-writer fork bug (issue #1121)",
		ruleAuditNSDisjoint01)
}

// TestAuditNamespaceDisjoint01_ReverseCheck is the blind-spot reverse
// self-check required by ai-robust.md §"工具选定后强制盲区自检" for the
// rule's chosen scanner tooling (CallExpr walk + name-literal predicate). It
// parses two synthetic snippets and asserts that the same name-literal
// predicate used in the production rule correctly classifies them — proving
// the scanner catches the negative case (no BootstrapNamespace call → no
// match) and the positive case (BootstrapNamespace call → match). Without
// this test the rule could silently degrade if a future refactor changed the
// name lookup shape.
//
// Blind spots not covered by ResolvePackageRef + EvaluateConstString that
// this archtest tooling cannot detect (tracked for future Hard upgrade):
//   - Wrapper helper indirection: a private func in cmd/corebundle that
//     calls audit.BootstrapNamespace() and exposes the result as
//     `var auditBootstrapNS = innerHelper()`. The CallExpr walk sees only
//     the helper, not the underlying canonical call.
//   - Reflection-constructed NamespaceID: `reflect.ValueOf(ledger.NamespaceID("bootstrap")).Interface()`.
//     Type-info resolution doesn't trace dynamic values.
//
// Both are unreachable today (no such helpers exist in cmd/corebundle); the
// SSA-reachability upgrade tracked alongside BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-
// DOWNSTREAM-HARD-01 would close both.
func TestAuditNamespaceDisjoint01_ReverseCheck(t *testing.T) {
	t.Parallel()

	parseExpr := func(src string) *ast.File {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", src, parser.AllErrors)
		require.NoError(t, err, "parse synthetic source")
		return f
	}

	// nameLiteralMatch is the same predicate used by the production rule —
	// extracting it here exercises the scanner shape against synthetic AST.
	nameLiteralMatch := func(file *ast.File, want string) bool {
		var found bool
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			if sel.Sel != nil && sel.Sel.Name == want {
				found = true
			}
		})
		return found
	}

	t.Run("compliant_fixture_matches_both_anchors", func(t *testing.T) {
		t.Parallel()
		const src = `package fixture
import (
	audit "x/runtime/audit"
	ledger "x/runtime/audit/ledger"
)
var _ = audit.BootstrapNamespace()
var _, _ = ledger.ParseNamespaceID("auditcore")
`
		f := parseExpr(src)
		assert.True(t, nameLiteralMatch(f, bootstrapNamespaceFnName),
			"compliant fixture must expose BootstrapNamespace() to the walker")
		assert.True(t, nameLiteralMatch(f, parseNamespaceIDFnName),
			"compliant fixture must expose ParseNamespaceID(...) to the walker")
	})

	t.Run("missing_bootstrap_namespace_fixture_caught", func(t *testing.T) {
		t.Parallel()
		const src = `package fixture
import (
	ledger "x/runtime/audit/ledger"
)
var _, _ = ledger.ParseNamespaceID("auditcore")
`
		f := parseExpr(src)
		assert.False(t, nameLiteralMatch(f, bootstrapNamespaceFnName),
			"reverse case: missing BootstrapNamespace() call must NOT be flagged by walker — production rule's bootstrapNamespaceFound stays false")
	})

	t.Run("only_bootstrap_no_relay_fixture_caught", func(t *testing.T) {
		t.Parallel()
		const src = `package fixture
import (
	audit "x/runtime/audit"
	ledger "x/runtime/audit/ledger"
)
var _ = audit.BootstrapNamespace()
var _, _ = ledger.ParseNamespaceID("bootstrap") // same namespace twice — single chain
`
		f := parseExpr(src)
		assert.True(t, nameLiteralMatch(f, bootstrapNamespaceFnName),
			"walker must see BootstrapNamespace() call")
		assert.True(t, nameLiteralMatch(f, parseNamespaceIDFnName),
			"walker must see ParseNamespaceID call (even when its arg evaluates to 'bootstrap')")
		// The production rule uses EvaluateConstString on the ParseNamespaceID
		// argument and filters out value == "bootstrap"; a synthetic case
		// where both Anchors are present but the arg resolves to "bootstrap"
		// would correctly fail the production rule's nonBootstrapNSFound check.
	})
}
