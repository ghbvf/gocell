// principal_kind_exhaustive_switch_test.go — forces every production switch on
// runtime/auth.PrincipalKind to explicitly handle every declared constant, so
// adding a new kind (e.g. PrincipalDevice for the multi-tenancy/ABAC epic)
// breaks CI until each switch gains a conscious branch.
//
//   - INVARIANT: PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01
//
// # What this guards
//
// PrincipalKind is an open enum that downstream authz / observability code
// switches on. A `default:` clause silently absorbs a newly-added kind
// (PrincipalDevice would fall through PrincipalKind.String()'s default and
// stringify as "unknown"), hiding the fanout obligation. This rule enumerates
// the full constant set of runtime/auth.PrincipalKind via go/types and requires
// every production switch whose tag is that type to list each constant as an
// explicit case. A `default:` clause does NOT excuse a missing constant — the
// point is to force a deliberate per-kind decision at every switch.
//
// # AI-robust rating (charter §"立项硬门槛")
//
//   - Medium. Detection is go/types-aware (the tag's *types.Named is resolved
//     by package path + type name, and case exprs are resolved to *types.Const
//     of that type — import aliases and dot-imports resolve identically), but it
//     is archtest-bound, not type-system-Hard: Go has no compile-time exhaustive
//     switch. The only Hard path would be a codegen funnel that generates the
//     String() method + a keyless-struct-literal compile-exhaustion table from a
//     single enum source (the SAGA-STATUS-FANOUT-COVERAGE-01 shape); that is
//     disproportionate for a five-value enum with a single fan-out site today.
//     This matches the spec's Medium rating (tasks.md T1.3). No gh upgrade issue
//     is opened: the codegen-funnel cost exceeds its benefit here, recorded in
//     this godoc rather than tracked as latent work.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - if/else-if chains comparing p.Kind (e.g. `if p.Kind == PrincipalUser`) are
//     NOT switches and are out of scope — they are not exhaustiveness carriers,
//     and a new kind that falls through such a chain is handled by the chain's
//     terminal branch by construction. The fanout review (#1339) confirmed the
//     only PrincipalKind switch today is String(); the if-chains in authz.go are
//     correct for PrincipalDevice without change (device is neither user nor
//     service). This blind spot is proven a non-false-positive by
//     TestPrincipalKindExhaustiveSwitch01_IfChainNotScanned (a non-exhaustive
//     if-chain fixture must yield ZERO diagnostics).
//   - Nested switches: only direct CaseClause children of the matched SwitchStmt
//     are read (sw.Body.List), so a PrincipalKind switch nested inside a case of
//     an unrelated switch is still evaluated on its own tag — no contamination.
//   - A switch with no tag (`switch { case x == PrincipalUser: }`) has no single
//     enum tag type and is not matched; such a form expresses arbitrary boolean
//     predicates, not enum exhaustiveness.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	principalKindPkgPath  = "github.com/ghbvf/gocell/runtime/auth"
	principalKindTypeName = "PrincipalKind"
)

// TestPrincipalKindExhaustiveSwitch01 asserts every production switch on
// runtime/auth.PrincipalKind explicitly covers all declared constants.
func TestPrincipalKindExhaustiveSwitch01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanPrincipalKindSwitches(p)
	})
	Report(t, "PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01", diags)
}

// TestPrincipalKindExhaustiveSwitch01_ReverseFixture is the anti-vacuity
// reverse self-check: a real type-checked fixture package with a non-exhaustive
// PrincipalKind switch MUST be flagged, and the missing kind must be named.
func TestPrincipalKindExhaustiveSwitch01_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/principalkindfixture/..."},
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			return scanPrincipalKindSwitches(p)
		})
	require.NotEmpty(t, diags,
		"non-exhaustive PrincipalKind switch in the fixture must be flagged; "+
			"an empty result means the scanner regressed (vacuous pass)")
	var joined strings.Builder
	for _, d := range diags {
		joined.WriteString(d.Message)
		joined.WriteByte('\n')
	}
	assert.Contains(t, joined.String(), "PrincipalDevice",
		"the missing-case diagnostic must name the newly-added kind")
}

// TestPrincipalKindExhaustiveSwitch01_IfChainNotScanned is the blind-spot
// reverse self-check: a non-exhaustive if/else-if chain over PrincipalKind (no
// switch) MUST yield zero diagnostics — proving the rule's switch-only scope is
// a deliberate non-false-positive, not an accidental gap.
func TestPrincipalKindExhaustiveSwitch01_IfChainNotScanned(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/principalkindifchainfixture/..."},
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			return scanPrincipalKindSwitches(p)
		})
	assert.Empty(t, diags,
		"if/else-if chains over PrincipalKind are out of scope and must not be flagged")
}

// scanPrincipalKindSwitches reports every production switch whose tag type is
// runtime/auth.PrincipalKind and whose explicit case set omits any declared
// constant of that type. A default clause is irrelevant (does not excuse a
// missing constant).
func scanPrincipalKindSwitches(p *Pass) []Diagnostic {
	var ds []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		EachInSubtree[ast.SwitchStmt](file, func(sw *ast.SwitchStmt) {
			if sw.Tag == nil {
				return
			}
			tv, ok := p.TypesInfo.Types[sw.Tag]
			if !ok || !isPrincipalKindType(tv.Type) {
				return
			}
			want := principalKindDeclaredConsts(tv.Type)
			if len(want) == 0 {
				return
			}
			got := principalKindCaseConsts(sw, p.TypesInfo)
			missing := make([]string, 0, len(want))
			for name := range want {
				if !got[name] {
					missing = append(missing, name)
				}
			}
			if len(missing) == 0 {
				return
			}
			sort.Strings(missing)
			ds = append(ds, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(sw.Pos()).Line,
				Message: fmt.Sprintf(
					"PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01: switch on %s.%s is missing explicit "+
						"case(s) for %s. A default clause does NOT excuse a missing enum constant — "+
						"add the case so a newly-added kind (e.g. PrincipalDevice) forces a conscious "+
						"branch at every switch.",
					principalKindPkgPath, principalKindTypeName, strings.Join(missing, ", ")),
			})
		})
	}
	return ds
}

// isPrincipalKindType reports whether t is the named type
// runtime/auth.PrincipalKind (resolved by package path + name, alias-proof).
func isPrincipalKindType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil &&
		obj.Pkg().Path() == principalKindPkgPath &&
		obj.Name() == principalKindTypeName
}

// principalKindDeclaredConsts enumerates, via go/types, every package-scope
// constant whose named type is runtime/auth.PrincipalKind. The scope is reached
// through the switch tag's own *types.Named, so enumeration works from any
// package that switches on PrincipalKind, not just runtime/auth itself.
func principalKindDeclaredConsts(t types.Type) map[string]bool {
	out := map[string]bool{}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return out
	}
	scope := named.Obj().Pkg().Scope()
	for _, name := range scope.Names() {
		if cn, ok := principalKindConstName(scope.Lookup(name)); ok {
			out[cn] = true
		}
	}
	return out
}

// principalKindCaseConsts returns the set of PrincipalKind constant names listed
// in the direct case clauses of sw (a default clause contributes nothing).
func principalKindCaseConsts(sw *ast.SwitchStmt, info *types.Info) map[string]bool {
	got := map[string]bool{}
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, e := range cc.List {
			if name, ok := principalKindCaseConstName(e, info); ok {
				got[name] = true
			}
		}
	}
	return got
}

// principalKindCaseConstName resolves a case expression (bare Ident or
// pkg.Selector) to a PrincipalKind constant name.
func principalKindCaseConstName(e ast.Expr, info *types.Info) (string, bool) {
	var id *ast.Ident
	switch x := e.(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		id = x.Sel
	default:
		return "", false
	}
	if id == nil {
		return "", false
	}
	return principalKindConstName(info.ObjectOf(id))
}

// principalKindConstName reports the constant's name when obj is a *types.Const
// whose named type is runtime/auth.PrincipalKind.
func principalKindConstName(obj types.Object) (string, bool) {
	c, ok := obj.(*types.Const)
	if !ok {
		return "", false
	}
	if !isPrincipalKindType(c.Type()) {
		return "", false
	}
	return c.Name(), true
}
