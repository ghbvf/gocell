// INVARIANT: SCAFFOLD-ID-FUNNEL-01
//
// scaffoldid.ScaffoldID is the typed scaffold identifier (SCAFFOLD-INPUT-
// CONTRACT-TYPED-ID-01). All ScaffoldID values reachable by production
// callers must be produced by scaffoldid.Parse so the AssemblyIDPattern
// (`^[a-z][a-z0-9]+$`) check is the single source of truth.
//
// The newtype is a string newtype, so Go allows explicit conversion via
// `scaffoldid.ScaffoldID("raw")` to construct a ScaffoldID without going
// through Parse — exactly the Soft-grade hole the funnel exists to close.
// This archtest scans every production .go file outside the scaffoldid
// package itself for that explicit conversion CallExpr and reports it as a
// funnel breach.
//
// AI-rebust: Hard via form uniqueness (charter §载体决策原则 string-typed
// concept funnel template, panic-funnel pattern from §Hard 范本第 2 条):
// callee resolves through *types.Info to scaffoldid.ScaffoldID (a *types.
// TypeName conversion call), arg is any value — but the form itself is the
// only path. Any cast outside the scaffoldid package fails CI; the
// hand-written tests we deliberately exempt (carve-out below) construct
// fixtures via the same typed conversion so the funnel guard tracks "is
// this production code?" rather than "is this a Parse call?".
//
// # Recognition: type-aware
//
// A CallExpr matches only when info.Uses[CallExpr.Fun] (or info.Types[fun])
// resolves to the *types.TypeName named (github.com/ghbvf/gocell/kernel/
// scaffoldid).ScaffoldID. Alias imports / dot-imports cannot evade.
//
// # Blind spots (declared per ai-collab §载体决策原则)
//
// CallExpr scanning misses:
//
//  1. `var x scaffoldid.ScaffoldID = "raw"` — untyped const conversion at
//     declaration / struct-literal time. This is the legitimate idiom for
//     test fixtures (no CallExpr) and benign in production where the const
//     literal is hand-vetted by the author. The funnel CANNOT close this
//     hole at archtest level because there is no syntactic marker; it is
//     covered conceptually by the requirement that production callers go
//     through scaffoldid.Parse for any non-const input.
//
//  2. Slice literals `[]scaffoldid.ScaffoldID{"a", "b"}` — same untyped
//     const conversion at slice-element time. Same justification as #1.
//
// The reverse self-check TestScaffoldIDFunnel_DetectsCastOutsidePackage
// constructs a CallExpr fixture inline so the archtest cannot silently
// pass by walking an empty AST.
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const (
	scaffoldidPkgPath = "github.com/ghbvf/gocell/kernel/scaffoldid"
	scaffoldIDType    = "ScaffoldID"
)

// callsScaffoldIDConversion reports whether call.Fun is a type conversion
// to scaffoldid.ScaffoldID, resolved through *types.Info.
func callsScaffoldIDConversion(info *types.Info, call *ast.CallExpr) bool {
	if info == nil || call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != scaffoldIDType {
		return false
	}
	obj, ok := info.Uses[sel.Sel]
	if !ok || obj == nil {
		return false
	}
	tn, ok := obj.(*types.TypeName)
	if !ok || tn.Pkg() == nil || tn.Pkg().Path() != scaffoldidPkgPath {
		return false
	}
	return true
}

// TestScaffoldIDFunnel_OnlyInScaffoldidPackage asserts that every
// production explicit-conversion CallExpr `scaffoldid.ScaffoldID(...)` lives
// inside the scaffoldid package itself. Test files (`*_test.go`) are
// exempted: fixtures legitimately construct ScaffoldID values without
// re-routing through Parse, and Parse coverage is asserted in
// kernel/scaffoldid/scaffoldid_test.go.
func TestScaffoldIDFunnel_OnlyInScaffoldidPackage(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			// Carve-out: the scaffoldid package itself is the funnel —
			// Parse internally returns ScaffoldID(raw) and that's the
			// authorized cast site.
			if strings.HasPrefix(rel, "kernel/scaffoldid/") {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if !callsScaffoldIDConversion(p.TypesInfo, call) {
					return
				}
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Message: "SCAFFOLD-ID-FUNNEL-01: explicit scaffoldid.ScaffoldID(...) " +
						"conversion outside kernel/scaffoldid/ — production callers must " +
						"construct ScaffoldID via scaffoldid.Parse so the AssemblyIDPattern " +
						"check is the single source of truth",
				})
			})
		}
		return out
	})
	Report(t, "SCAFFOLD-ID-FUNNEL-01", diags)
}
