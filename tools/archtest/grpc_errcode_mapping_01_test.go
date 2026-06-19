//go:build archtest

// Package archtest — grpc_errcode_mapping_01_test.go
//
//   - INVARIANT: GRPC-ERRCODE-MAPPING-01
//
// # What this guards
//
// The `toGRPCCode(k errcode.Kind)` function in
// runtime/grpc/interceptor/errcode_mapping.go maps every errcode.Kind to a
// grpc/codes.Code. If a new Kind constant is added to pkg/errcode but
// toGRPCCode's switch is not updated, the new kind falls through to the
// `default:` clause (codes.Internal) — silently. This rule enumerates the
// full constant set of errcode.Kind via go/types and requires the interceptor
// package's Kind switch to list each constant as an explicit case. A `default:`
// clause does NOT excuse a missing constant — the point is to force a
// deliberate per-kind gRPC code decision at every release.
//
// # Package scoping (deliberate design)
//
// The scan is SCOPED to the gRPC interceptor package and the fixture directory.
// This is required to avoid false-positives on other switches over errcode.Kind
// that legitimately use `default:` — for example, Kind.Status() and
// Kind.PublicCode() in pkg/errcode/status.go. Those switches cover the
// KindInternal zero-value via `default:` by design (fail-closed); flagging them
// would be a false positive. Scoping the rule to its ONLY enforcement target
// (the gRPC errcode→codes projection switch) is the deliberate design decision
// documented here as a blind-spot note (charter §"强制盲区自检"):
//
//   - A new switch over errcode.Kind added to the interceptor package with a
//     default clause absorbing a missing kind would be flagged by this rule.
//   - A switch over errcode.Kind added to a DIFFERENT package outside the scope
//     is NOT flagged — the rule covers only the interceptor package's mapping.
//     This is acceptable: the mapping funnel lives in one file and is the sole
//     enforcement target.
//
// # AI-robust rating (charter §"立项硬门槛")
//
//   - Medium (permanent Go ceiling). Detection is go/types-aware: the switch tag
//     type is resolved by package path + type name (errcode.Kind), case exprs are
//     resolved to *types.Const of that type — import aliases resolve identically.
//     Hard is not reachable: Go has no compile-time exhaustive switch enforcement.
//     This matches the same ceiling class as PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Switches outside the scoped packages are not evaluated — see §Package scoping.
//   - A `switch k { case A, B: ... }` with two kinds in one case clause is handled:
//     each case expression is enumerated independently.
//   - No gh Hard-upgrade issue is opened: the codegen funnel cost exceeds its
//     benefit for a 14-value enum with one enforcement site.
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
	// errcodeKindTypeName is the exported type name of the mapping enum.
	// errcodePkgPath is already declared in panic_invariants.go at package scope.
	errcodeKindTypeName = "Kind"

	// grpcErrcodeMappingInterceptorPkg is the package the rule scopes to.
	grpcErrcodeMappingInterceptorPkg = PlatformFrameworkModulePath + "/runtime/grpc/interceptor"

	// grpcErrcodeMappingFixturePkg is the RED fixture package.
	grpcErrcodeMappingFixturePkg = "./tools/archtest/internal/grpcerrcodemappingfixture"
)

// TestGRPCErrcodeMapping01_Production asserts that every production switch in
// the gRPC interceptor package whose tag type is errcode.Kind explicitly covers
// all declared Kind constants. A default clause does NOT excuse a missing constant.
func TestGRPCErrcodeMapping01_Production(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		// Scope: interceptor package only.
		if p.Pkg.Path() != grpcErrcodeMappingInterceptorPkg {
			return nil
		}
		return scanErrcodeKindSwitches(p)
	})
	Report(t, "GRPC-ERRCODE-MAPPING-01", diags)
}

// TestGRPCErrcodeMapping01_ReverseFixture is the anti-vacuity reverse self-check:
// a real type-checked fixture package with a non-exhaustive errcode.Kind switch
// MUST be flagged, and the missing kinds must be named.
func TestGRPCErrcodeMapping01_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{grpcErrcodeMappingFixturePkg}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			return scanErrcodeKindSwitches(p)
		})

	require.NotEmpty(t, diags,
		"GRPC-ERRCODE-MAPPING-01 reverse fixture: non-exhaustive errcode.Kind switch "+
			"must be flagged; an empty result means the scanner regressed (vacuous pass)")

	// The fixture omits KindUnprocessable and KindGone — both must be named.
	var joined strings.Builder
	for _, d := range diags {
		joined.WriteString(d.Message)
		joined.WriteByte('\n')
	}
	msg := joined.String()
	assert.Contains(t, msg, "KindUnprocessable",
		"reverse fixture: missing-case diagnostic must name KindUnprocessable")
	assert.Contains(t, msg, "KindGone",
		"reverse fixture: missing-case diagnostic must name KindGone")
}

// scanErrcodeKindSwitches reports every switch in p whose tag type is
// errcode.Kind (resolved by package path + type name) and whose explicit case
// set omits any declared constant of that type.
func scanErrcodeKindSwitches(p *Pass) []Diagnostic {
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
			if !ok || !isErrcodeKindType(tv.Type) {
				return
			}
			want := errcodeKindDeclaredConsts(tv.Type)
			if len(want) == 0 {
				return
			}
			got := errcodeKindCaseConsts(sw, p.TypesInfo)
			var missing []string
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
					"GRPC-ERRCODE-MAPPING-01: switch on %s.%s in the gRPC interceptor package "+
						"is missing explicit case(s) for %s. A default clause does NOT excuse a "+
						"missing Kind constant — add the case so a newly-added kind forces a "+
						"conscious grpc/codes.Code mapping in toGRPCCode.",
					errcodePkgPath, errcodeKindTypeName, strings.Join(missing, ", "),
				),
			})
		})
	}
	return ds
}

// isErrcodeKindType reports whether t is the named type errcode.Kind (by pkg
// path + type name, alias-proof).
func isErrcodeKindType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil &&
		obj.Pkg().Path() == errcodePkgPath &&
		obj.Name() == errcodeKindTypeName
}

// errcodeKindDeclaredConsts enumerates every package-scope constant whose named
// type is errcode.Kind via go/types.
func errcodeKindDeclaredConsts(t types.Type) map[string]bool {
	out := map[string]bool{}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return out
	}
	scope := named.Obj().Pkg().Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		c, ok := obj.(*types.Const)
		if !ok {
			continue
		}
		if !isErrcodeKindType(c.Type()) {
			continue
		}
		out[c.Name()] = true
	}
	return out
}

// errcodeKindCaseConsts returns the set of errcode.Kind constant names listed in
// the direct case clauses of sw. Default clauses contribute nothing.
func errcodeKindCaseConsts(sw *ast.SwitchStmt, info *types.Info) map[string]bool {
	got := map[string]bool{}
	EachInChildren[ast.CaseClause](sw.Body, func(cc *ast.CaseClause) {
		for _, e := range cc.List {
			var id *ast.Ident
			switch x := e.(type) {
			case *ast.Ident:
				id = x
			case *ast.SelectorExpr:
				id = x.Sel
			}
			if id == nil {
				continue
			}
			obj := info.ObjectOf(id)
			c, ok := obj.(*types.Const)
			if !ok || !isErrcodeKindType(c.Type()) {
				continue
			}
			got[c.Name()] = true
		}
	})
	return got
}
