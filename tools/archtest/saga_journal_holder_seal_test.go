// Package archtest verifies SAGA-JOURNAL-HOLDER-SEAL-01.
//
// INVARIANT: SAGA-JOURNAL-HOLDER-SEAL-01
//
// Only runtime/saga.Coordinator may hold a kernel/saga/journal.Journal field.
// Any other struct in runtime/saga declaring a field of that interface type is
// a violation.
//
// # Why this lives in PR-03 (not PR-08 per plan)
//
// As soon as the type exists, the invariant is statically lockable. Earlier
// locking catches PR-04/05/06 regressions immediately. AI-robust evaluation:
// upstream Medium (archtest A1 typed-aware scan locks holder-struct identity
// within package), downstream N/A (field-shape invariant, not a callsite
// invariant — no caller allowlist needed).
//
// # Blind-spot reverse self-test
//
//   - B1: production code MUST NOT introduce a `type X = journal.Journal` alias
//     in runtime/saga (would let a new struct hold `X` while still being the
//     same interface — A1 would miss it because the AST alias TypeSpec has an
//     Assign token and the alias RHS selector is the escape path).
//
// # Coverage notes (blind spots of A1 + B1)
//
//   - A1 detects fields whose resolved type is the named interface
//     kernel/saga/journal.Journal. A type alias `type J = journal.Journal` in a
//     *different* package (outside runtime/saga) that is then imported and used
//     as a field type in runtime/saga is not covered by B1 (B1 only scans
//     runtime/saga itself). This is an accepted residual blind spot: cross-package
//     aliases in Go are rare and the types resolution in A1 chases through aliases
//     at the types.Type level (Named.Obj().Pkg() and Name() are canonical after
//     type-checking, even through aliases in other packages).
//   - B1 only scans non-test files in runtime/saga; test files may alias the
//     type freely.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

// journalInterfacePkgPath is the canonical import path of the package that
// declares the Journal interface. Hardcoded rather than derived from go.mod
// because it is load-bearing: a rename of this package path would be a
// contract break requiring a deliberate update here.
const journalInterfacePkgPath = "github.com/ghbvf/gocell/kernel/saga/journal"

// journalInterfaceTypeName is the type name of the interface within the package.
const journalInterfaceTypeName = "Journal"

// allowedSagaJournalHolder is the single struct in runtime/saga permitted to
// hold a journal.Journal field.
const allowedSagaJournalHolder = "Coordinator"

// sagaJournalHolderSealRule is the rule ID prefixed to every diagnostic message.
const sagaJournalHolderSealRule = "SAGA-JOURNAL-HOLDER-SEAL-01"

// fieldTypeIsJournal reports whether the field's resolved type (via go/types)
// is kernel/saga/journal.Journal. It handles:
//   - direct field type `journal.Journal` (SelectorExpr in AST)
//   - pointer to interface `*journal.Journal` (unusual but possible)
//   - type aliases that resolve to journal.Journal (types resolve through aliases
//     at the types.Type level, so Named.Obj().Pkg().Path() is canonical)
//
// Returns false if TypesInfo is nil or the expression cannot be resolved.
func fieldTypeIsJournal(info *types.Info, expr ast.Expr) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	return resolvedTypeIsJournal(tv.Type)
}

// resolvedTypeIsJournal checks whether t (possibly wrapped in a pointer or
// alias) resolves to the kernel/saga/journal.Journal named interface.
func resolvedTypeIsJournal(t types.Type) bool {
	if t == nil {
		return false
	}
	// Unwrap a pointer: *journal.Journal is not idiomatic but we cover it.
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	// After un-aliasing at the types level, we expect a *types.Named.
	// types.Unalias is available in Go 1.22+; fallback: walk through Named.
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil {
		return false
	}
	if obj.Name() != journalInterfaceTypeName {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	return pkg.Path() == journalInterfacePkgPath
}

// TestSagaJournalHolderSeal_A1_OnlyCoordinatorHoldsJournal scans the
// runtime/saga production source for any struct declaring a field whose
// resolved type is kernel/saga/journal.Journal. Asserts that the owning
// TypeSpec.Name == "Coordinator".
//
// Uses RunTyped (not Run) so that go/types can resolve field types across
// package boundaries — a pure AST scan cannot distinguish `journal.Journal`
// from any other selector expression named "Journal" without type information.
func TestSagaJournalHolderSeal_A1_OnlyCoordinatorHoldsJournal(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/saga/..."}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}

			EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					return
				}
				holderName := ts.Name.Name
				for _, field := range st.Fields.List {
					if !fieldTypeIsJournal(p.TypesInfo, field.Type) {
						continue
					}
					if holderName == allowedSagaJournalHolder {
						continue
					}
					pos := p.Fset.Position(field.Pos())
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: struct %q holds a journal.Journal field; only %q may hold this interface type",
							sagaJournalHolderSealRule, holderName, allowedSagaJournalHolder),
					})
				}
			})
		}
		return out
	})

	Report(t, sagaJournalHolderSealRule+"-A1", diags)
}

// TestSagaJournalHolderSeal_BlindSpot_B1_NoAliasInRuntimeSaga ensures that
// production non-test files in runtime/saga do not introduce a type alias of
// the form `type X = journal.Journal`. Such an alias would let a new struct
// declare a field of type X — which is structurally identical to journal.Journal
// — while evading A1's exact-name check (if A1 were implemented as a pure AST
// string match rather than a types-resolved check). Although A1 uses go/types
// resolution (which chases through aliases), B1 is retained as defense-in-depth:
// it catches the alias declaration itself before any struct can use it.
//
// B1 uses Run (AST-only) because detecting `type X = journal.Journal` is a
// pure syntactic check: an alias TypeSpec has a valid ts.Assign token, and the
// RHS is a SelectorExpr whose X.Name == "journal" and Sel.Name == "Journal".
// No cross-package resolution is needed for the alias declaration site itself.
func TestSagaJournalHolderSeal_BlindSpot_B1_NoAliasInRuntimeSaga(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"runtime/saga"})

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}

			EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
				// An alias has a valid Assign token.
				if !ts.Assign.IsValid() {
					return
				}
				// Check whether the aliased type is journal.Journal via the
				// selector expression `journal.Journal`.
				sel, ok := ts.Type.(*ast.SelectorExpr)
				if !ok {
					return
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return
				}
				if id.Name != "journal" || sel.Sel.Name != journalInterfaceTypeName {
					return
				}
				pos := p.Fset.Position(ts.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"%s-B1: type alias %q = journal.Journal in runtime/saga; "+
							"remove the alias and reference journal.Journal directly — "+
							"aliases create an evasion path for the single-holder invariant",
						sagaJournalHolderSealRule, ts.Name.Name),
				})
			})
		}
		return out
	})

	Report(t, sagaJournalHolderSealRule+"-B1", diags)
}
