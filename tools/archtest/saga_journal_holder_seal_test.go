// Package archtest verifies SAGA-JOURNAL-HOLDER-SEAL-01.
//
// INVARIANT: SAGA-JOURNAL-HOLDER-SEAL-01
//
// Three field-shape rules over runtime/saga production structs:
//
//  1. No struct may hold a Heartbeat-bearing journal interface as a persisted
//     field — i.e. neither kernel/saga/journal.Journal (the full interface) nor
//     kernel/saga/journal.Heartbeater. A persisted Heartbeat-capable field is
//     exactly what a centralized heartbeat loop needs (the #1181 anti-pattern);
//     it is forbidden everywhere, Coordinator included.
//  2. kernel/saga/journal.JournalCore (the Heartbeat-free core) may be held only
//     by runtime/saga.Coordinator. Any other holder is a violation.
//  3. No struct may persist a Heartbeater-SHAPED func value as a field
//     (func(context.Context, idutil.SafeID, idutil.SafeID, time.Duration)
//     (bool, error)). A persisted heartbeat func is the func-value equivalent of
//     a journal.Heartbeater field: it lets a centralized loop be reconstructed
//     from a heartbeat func handed in from OUTSIDE runtime/saga (where the
//     `.Heartbeat` selector is beyond SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 A1's
//     scope). Forbidden everywhere, Coordinator included. (rule 3 / F3)
//
// The full journal.Journal still exists transiently as the NewCoordinator
// parameter handed straight to executor.NewExecutor (the sanctioned per-step
// heartbeat funnel); that is a parameter, not a field, so it never trips rule 1.
//
// # Relationship to the JournalCore split (#1209)
//
// Before #1209 this seal tracked the flat journal.Journal interface, and
// Coordinator held journal.Journal directly. #1209 split journal.Journal into
// JournalCore (6 methods) + Heartbeater (Heartbeat) and narrowed
// Coordinator.journal to JournalCore so c.journal.Heartbeat(...) is a compile
// error — that is what upgrades SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01's upstream
// to Hard (see that archtest). This seal is the field-shape complement: it keeps
// the narrow type single-held and bans any re-introduction of a Heartbeat-bearing
// field.
//
// # AI-robust rating
//
// Upstream Medium: a typed-aware go/types scan locks holder-struct identity
// within the package; a new non-Coordinator JournalCore field, any
// Heartbeat-bearing journal interface field, or any Heartbeater-shaped func
// field (rule 3) is rejected at archtest time, not by the compiler.
// Downstream N/A: this is a field-shape invariant, not a callsite invariant —
// there is no caller allowlist.
//
// The JournalCore split does NOT make THIS seal Hard: a non-Coordinator struct
// declaring a JournalCore field is still source-expressible (the archtest, not
// the type system, rejects it). The upstream-Hard path for the holder seal
// (envelope / sealed-construction so a non-Coordinator field is compile-time
// inexpressible) is tracked in gh issue #982; post-#1209 #982 tracks JournalCore
// rather than the full Journal. (Historic note: this godoc previously cited
// #981, which is an unrelated rule — SAGA-STEP-COMPENSATE-PURE-01.)
//
// # Blind-spot self-test (AI-robust §"工具选定后强制盲区自检")
//
// A1 uses go/types field-type resolution; B1 is an AST-only alias-declaration
// scan. Forms outside those tools' reach:
//
//   - B1 (reverse self-test): a `type X = journal.{Journal,JournalCore,Heartbeater}`
//     alias in runtime/saga would let a struct hold `X` evading a pure-AST name
//     match. A1's go/types resolution chases through aliases via types.Unalias
//     (mandatory on Go 1.23+ where an alias is *types.Alias), but B1 catches the
//     alias declaration itself before any struct uses it. Covered.
//   - Embedded (anonymous) field `struct { journal.Heartbeater }`: ast.StructType
//     Fields.List includes embedded fields (Names empty, Type set), so A1
//     classifies it. Covered.
//   - Pointer field `*journal.JournalCore`: classifyResolvedJournalType unwraps
//     one pointer level. Covered. Double pointer `**journal.JournalCore` is a
//     degenerate, non-idiomatic form — accepted residual blind spot.
//   - Cross-package alias `type J = journal.JournalCore` declared OUTSIDE
//     runtime/saga then imported and used as a field type inside runtime/saga:
//     A1 classifies it correctly because classifyResolvedJournalType calls
//     types.Unalias (required on Go 1.23+ where an alias is *types.Alias, not
//     *types.Named). B1 (which only scans runtime/saga) does not flag the foreign
//     declaration, but A1 resolves the field type regardless. Accepted residual
//     for B1; A1-covered.
//   - Local interface that re-declares the Heartbeat shape, e.g.
//     `type beat interface { Heartbeat(...) }` held as a field: a new named type,
//     NOT journal.*, so this seal does not classify it. Accepted residual —
//     composed with SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01, whose A1 callsite scan
//     is signature-shape (not name) based and flags any actual .Heartbeat(...)
//     call in runtime/saga regardless of the holder type. Holding without calling
//     is inert; calling is caught there.
//   - B1 resolves the journal package's local binding from each file's imports
//     (journalPackageLocalNames), so an import alias `import sagajournal "…/journal"`
//     is covered (F2 fix). A dot-import `import . "…/journal"` would make the
//     alias RHS a bare Ident (`type X = JournalCore`, no SelectorExpr) — not
//     matched by B1; accepted residual (dot-imports are non-idiomatic and A1
//     still resolves any field that actually uses such an alias). B1 only scans
//     non-test files in runtime/saga; test files may alias freely.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// journalInterfacePkgPath is the canonical import path of the package that
// declares the Journal / JournalCore / Heartbeater interfaces. Hardcoded rather
// than derived from go.mod because it is load-bearing: a rename of this package
// path would be a contract break requiring a deliberate update here.
const journalInterfacePkgPath = "github.com/ghbvf/gocell/kernel/saga/journal"

// journalInterfaceTypeName / heartbeaterInterfaceTypeName name the two
// Heartbeat-bearing interfaces that may not be persisted as a field anywhere in
// runtime/saga production code (rule 1).
const (
	journalInterfaceTypeName     = "Journal"
	heartbeaterInterfaceTypeName = "Heartbeater"
)

// journalCoreInterfaceTypeName names the Heartbeat-free core interface that only
// the Coordinator may hold as a field (rule 2).
const journalCoreInterfaceTypeName = "JournalCore"

// allowedSagaJournalHolder is the single struct in runtime/saga permitted to
// hold a journal.JournalCore field.
const allowedSagaJournalHolder = "Coordinator"

// sagaJournalHolderSealRule is the rule ID prefixed to every diagnostic message.
const sagaJournalHolderSealRule = "SAGA-JOURNAL-HOLDER-SEAL-01"

// journalFieldKind classifies a struct field's resolved type against the three
// journal-package interfaces this seal cares about.
type journalFieldKind int

const (
	journalFieldNone        journalFieldKind = iota // not a journal-package interface
	journalFieldFull                                // journal.Journal (Heartbeat-bearing)
	journalFieldHeartbeater                         // journal.Heartbeater (Heartbeat-bearing)
	journalFieldCore                                // journal.JournalCore (Heartbeat-free)
)

// classifyJournalFieldType resolves the field expression via go/types and
// classifies it. Returns journalFieldNone when TypesInfo is nil or the type is
// not one of the three journal-package interfaces.
func classifyJournalFieldType(info *types.Info, expr ast.Expr) journalFieldKind {
	if info == nil {
		return journalFieldNone
	}
	tv, ok := info.Types[expr]
	if !ok {
		return journalFieldNone
	}
	return classifyResolvedJournalType(tv.Type)
}

// classifyResolvedJournalType checks whether t (possibly wrapped in one pointer
// or resolved through a type alias) is journal.Journal, journal.Heartbeater, or
// journal.JournalCore.
//
// On Go 1.23+ (gotypesalias=1, the default — this module is on go 1.25), a type
// alias materializes as *types.Alias, NOT transparently as the aliased
// *types.Named. A bare t.(*types.Named) assertion therefore MISSES alias-typed
// fields (a field of `type J = journal.JournalCore` resolves to *types.Alias and
// the assertion fails). types.Unalias collapses an alias to its underlying type
// so the Obj().Pkg().Path()/Name() check below is canonical regardless of how
// many alias / pointer layers wrap the field type (go/types.Unalias expands a
// type to the one it denotes after resolving package-level aliases).
func classifyResolvedJournalType(t types.Type) journalFieldKind {
	if t == nil {
		return journalFieldNone
	}
	// Collapse a top-level alias (`type J = journal.JournalCore`) before the
	// pointer probe, then again after unwrapping a pointer (`*J`, or
	// `type J = *journal.JournalCore`), so every alias⇄pointer ordering resolves.
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok {
		return journalFieldNone
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != journalInterfacePkgPath {
		return journalFieldNone
	}
	switch obj.Name() {
	case journalInterfaceTypeName:
		return journalFieldFull
	case heartbeaterInterfaceTypeName:
		return journalFieldHeartbeater
	case journalCoreInterfaceTypeName:
		return journalFieldCore
	}
	return journalFieldNone
}

// journalFieldSealDiag applies the two seal rules to a single struct field and
// returns a diagnostic when violated.
func journalFieldSealDiag(p *Pass, rel, holderName string, field *ast.Field) (Diagnostic, bool) {
	kind := classifyJournalFieldType(p.TypesInfo, field.Type)
	if kind == journalFieldNone {
		return Diagnostic{}, false
	}
	// Rule 2: JournalCore is allowed, but only on the Coordinator.
	if kind == journalFieldCore && holderName == allowedSagaJournalHolder {
		return Diagnostic{}, false
	}
	pos := p.Fset.Position(field.Pos())
	return Diagnostic{Rel: rel, Line: pos.Line, Message: journalFieldSealMessage(kind, holderName)}, true
}

// journalFieldSealMessage renders the diagnostic text for a violating field.
func journalFieldSealMessage(kind journalFieldKind, holderName string) string {
	if kind == journalFieldCore {
		return fmt.Sprintf(
			"%s: struct %q holds a journal.JournalCore field; only %q may hold it",
			sagaJournalHolderSealRule, holderName, allowedSagaJournalHolder)
	}
	// journalFieldFull / journalFieldHeartbeater — a Heartbeat-bearing field.
	return fmt.Sprintf(
		"%s: struct %q holds a Heartbeat-bearing journal interface field "+
			"(journal.Journal or journal.Heartbeater); no struct in runtime/saga may "+
			"persist a Heartbeat-capable field — hold journal.JournalCore instead. The "+
			"full Journal exists transiently only as the NewCoordinator parameter handed "+
			"to executor.NewExecutor (the sanctioned per-step heartbeat funnel).",
		sagaJournalHolderSealRule, holderName)
}

// heartbeatFuncFieldDiag (rule 3) flags a struct field whose type is a func —
// anonymous, named, aliased, or pointer-to-func — matching the Heartbeater
// signature shape. Persisting such a callable is the func-value equivalent of
// holding a journal.Heartbeater field: a centralized heartbeat loop can be
// reconstructed from a heartbeat func passed into the constructor from OUTSIDE
// runtime/saga, where the `.Heartbeat` selector is beyond
// SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 A1's scope. Closing the field-
// persistence path here blocks the loop (a loop needs a persisted callable; a
// bare constructor closure capturing the param is the irreducible residual, same
// class as that archtest's NewCoordinator pass-through window). No struct in
// runtime/saga — Coordinator included — may persist a heartbeat-shaped func.
func heartbeatFuncFieldDiag(p *Pass, rel, holderName string, field *ast.Field) (Diagnostic, bool) {
	if p.TypesInfo == nil {
		return Diagnostic{}, false
	}
	tv, ok := p.TypesInfo.Types[field.Type]
	if !ok {
		return Diagnostic{}, false
	}
	// Collapse alias + one pointer level (mirrors classifyResolvedJournalType),
	// then require the underlying type to be a func signature of the heartbeat
	// shape. Interface fields (Underlying = *types.Interface) never match here —
	// they are journal-package interfaces handled by classifyJournalFieldType.
	t := types.Unalias(tv.Type)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	sig, ok := t.Underlying().(*types.Signature)
	if !ok || !signatureMatchesHeartbeaterShape(sig) {
		return Diagnostic{}, false
	}
	pos := p.Fset.Position(field.Pos())
	return Diagnostic{
		Rel:  rel,
		Line: pos.Line,
		Message: fmt.Sprintf(
			"%s: struct %q holds a Heartbeater-shaped func field "+
				"(func(context.Context, idutil.SafeID, idutil.SafeID, time.Duration) (bool, error)); "+
				"no struct in runtime/saga may persist a Heartbeat-capable callable (interface OR func) "+
				"— a persisted heartbeat func reconstructs the centralized-loop anti-pattern from a value "+
				"passed in from outside runtime/saga. Funnel per-step heartbeat through executor.",
			sagaJournalHolderSealRule, holderName),
	}, true
}

// TestSagaJournalHolderSeal_A1_OnlyCoordinatorHoldsJournal scans runtime/saga
// production source for struct fields whose resolved type is a journal-package
// interface, applying both seal rules (see package godoc):
//   - journal.Journal / journal.Heartbeater field anywhere → violation
//   - journal.JournalCore field outside Coordinator → violation
//
// Uses RunTyped (not Run) so go/types can resolve field types across package
// boundaries — a pure AST scan cannot distinguish `journal.JournalCore` from any
// other selector named "JournalCore" without type information.
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
					if d, ok := journalFieldSealDiag(p, rel, holderName, field); ok {
						out = append(out, d)
						continue
					}
					if d, ok := heartbeatFuncFieldDiag(p, rel, holderName, field); ok {
						out = append(out, d)
					}
				}
			})
		}
		return out
	})

	Report(t, sagaJournalHolderSealRule+"-A1", diags)
}

// journalPackageLocalNames returns the set of local identifiers in file bound
// to the journal interface package (journalInterfacePkgPath): the default
// package name for a plain import, plus any explicit import alias
// (`import sagajournal "…/journal"`). Blank (`_`) and dot (`.`) imports are not
// usable as a `pkg.Type` selector base and are excluded — a dot-imported
// `type X = JournalCore` is a bare-Ident form (no SelectorExpr) noted as a
// residual in the package godoc.
func journalPackageLocalNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != journalInterfacePkgPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names[path.Base(journalInterfacePkgPath)] = true // default name: "journal"
		case imp.Name.Name == "_" || imp.Name.Name == ".":
			// not usable as a selector base; skip
		default:
			names[imp.Name.Name] = true
		}
	}
	return names
}

// journalInterfaceAliasName reports the journal interface name aliased by ts if
// ts is `type X = <localName>.{Journal,JournalCore,Heartbeater}` where localName
// is any local binding of the journal package (journalLocalNames), else
// ("", false). Resolving via journalLocalNames rather than a hardcoded "journal"
// closes the import-alias evasion: `import sagajournal "…/journal"` followed by
// `type X = sagajournal.JournalCore` is now flagged.
func journalInterfaceAliasName(ts *ast.TypeSpec, journalLocalNames map[string]bool) (string, bool) {
	// An alias has a valid Assign token.
	if !ts.Assign.IsValid() {
		return "", false
	}
	sel, ok := ts.Type.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || !journalLocalNames[id.Name] {
		return "", false
	}
	switch sel.Sel.Name {
	case journalInterfaceTypeName, journalCoreInterfaceTypeName, heartbeaterInterfaceTypeName:
		return sel.Sel.Name, true
	}
	return "", false
}

// TestSagaJournalHolderSeal_BlindSpot_B1_NoAliasInRuntimeSaga ensures production
// non-test files in runtime/saga do not introduce a type alias of the form
// `type X = journal.Journal` / `journal.JournalCore` / `journal.Heartbeater`.
// Such an alias would let a new struct declare a field of type X — structurally
// identical to the aliased interface — while evading A1's exact-name check (if A1
// were a pure AST string match rather than a types-resolved check). A1 uses
// go/types resolution (which chases through aliases), so B1 is defense-in-depth:
// it catches the alias declaration itself before any struct can use it.
//
// B1 uses Run (AST-only) because detecting the alias is a pure syntactic check:
// an alias TypeSpec has a valid ts.Assign token and the RHS is a SelectorExpr
// whose X.Name binds to the journal package. The binding is resolved from the
// file's own import specs (journalPackageLocalNames), so an import alias
// (`import sagajournal "…/journal"`) is covered without cross-package type
// resolution at the declaration site.
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
			journalNames := journalPackageLocalNames(file)
			if len(journalNames) == 0 {
				continue // file does not import the journal package
			}

			EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
				aliased, ok := journalInterfaceAliasName(ts, journalNames)
				if !ok {
					return
				}
				pos := p.Fset.Position(ts.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"%s-B1: type alias %q = journal.%s in runtime/saga; remove the alias "+
							"and reference the journal interface directly — aliases create an "+
							"evasion path for the holder-seal invariant",
						sagaJournalHolderSealRule, ts.Name.Name, aliased),
				})
			})
		}
		return out
	})

	Report(t, sagaJournalHolderSealRule+"-B1", diags)
}

// TestSagaJournalHolderSeal_BlindSpot_B1_MatcherNonVacuous proves the B1
// alias-matcher is wired and non-vacuous. B1's production scan reports zero
// violations today (by design), so — unlike NO-HEARTBEAT-LOOP's B1, which can
// assert "executor has >=1 callsite" — it cannot demonstrate non-vacuity from
// production source. Instead this exercises journalInterfaceAliasName against a
// synthetic AST covering every form: the three sealed alias names must match,
// and non-aliases / wrong package / non-selector / definition (non-alias) forms
// must NOT. If the matcher silently stopped firing, B1 would pass vacuously and
// this test catches it.
//
// Case H (`sagajournal.JournalCore`) locks the F2 fix: the journal package
// imported under a non-default local name must still match, while case E
// (`other.Journal`, a name NOT bound to the journal package) must not.
func TestSagaJournalHolderSeal_BlindSpot_B1_MatcherNonVacuous(t *testing.T) {
	t.Parallel()

	const src = `package x
type A = journal.Journal
type B = journal.JournalCore
type C = journal.Heartbeater
type D = journal.Other
type E = other.Journal
type F journal.Journal
type G = SomethingElse
type H = sagajournal.JournalCore
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}

	// journalNames models a file that imports the journal package both plainly
	// (local name "journal") and under an alias (`import sagajournal "…/journal"`).
	// "other" is deliberately absent — it is NOT a binding of the journal pkg.
	journalNames := map[string]bool{"journal": true, "sagajournal": true}

	// typeName -> expected aliased journal interface name ("" = must NOT match).
	want := map[string]string{
		"A": journalInterfaceTypeName,     // type X = journal.Journal
		"B": journalCoreInterfaceTypeName, // type X = journal.JournalCore
		"C": heartbeaterInterfaceTypeName, // type X = journal.Heartbeater
		"D": "",                           // journal.Other — not a sealed name
		"E": "",                           // other.Journal — name not bound to journal pkg
		"F": "",                           // definition, not an alias (no '=')
		"G": "",                           // not a selector expression
		"H": journalCoreInterfaceTypeName, // type X = sagajournal.JournalCore (import alias)
	}

	seen := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			exp, tracked := want[ts.Name.Name]
			if !tracked {
				continue
			}
			seen[ts.Name.Name] = true
			aliased, matched := journalInterfaceAliasName(ts, journalNames)
			if exp == "" {
				if matched {
					t.Errorf("journalInterfaceAliasName(%s) = (%q, true); want no match",
						ts.Name.Name, aliased)
				}
				continue
			}
			if !matched || aliased != exp {
				t.Errorf("journalInterfaceAliasName(%s) = (%q, %v); want (%q, true)",
					ts.Name.Name, aliased, matched, exp)
			}
		}
	}

	// Non-vacuity: every positive case (incl. the import-aliased H) must have
	// been exercised — otherwise the fixture or the parse silently skipped them.
	for _, name := range []string{"A", "B", "C", "H"} {
		if !seen[name] {
			t.Errorf("synthetic fixture did not exercise positive case %q — "+
				"B1 matcher self-test is vacuous", name)
		}
	}
}

// TestSagaJournalHolderSeal_A1_AliasFieldResolvedViaUnalias is the F1 regression:
// classifyResolvedJournalType must resolve a struct field whose type is a
// package-level alias to journal.JournalCore. On Go 1.23+ (gotypesalias=1, this
// module is on go 1.25) the field type is *types.Alias; without types.Unalias
// the *types.Named assertion fails and the alias-typed holder silently evades
// the seal — defeating the holder seal with a one-line alias.
//
// The fixture (testdata/saga_journal_alias_fixtures/aliasholder) has two
// non-Coordinator holders — one direct, one via alias — and BOTH must be flagged.
// Pre-fix the alias holder is missed (this test fails); post-fix both surface.
func TestSagaJournalHolderSeal_A1_AliasFieldResolvedViaUnalias(t *testing.T) {
	t.Parallel()

	diags := RunTypedFixture(t, FixtureOpts{},
		[]string{"./tools/archtest/testdata/saga_journal_holder_seal_fixtures/aliasholder"},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, file := range p.Files {
				rel := filepath.ToSlash(p.Rel(file))
				EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					// holderName is never "Coordinator" for fixture structs, so
					// the rule-2 JournalCore allowance never applies — every
					// JournalCore field (direct or aliased) must surface.
					for _, field := range st.Fields.List {
						if d, ok := journalFieldSealDiag(p, rel, ts.Name.Name, field); ok {
							out = append(out, d)
						}
					}
				})
			}
			return out
		})

	var directFlagged, aliasFlagged bool
	for _, d := range diags {
		if strings.Contains(d.Message, "HolderDirect") {
			directFlagged = true
		}
		if strings.Contains(d.Message, "HolderViaAlias") {
			aliasFlagged = true
		}
	}
	if !directFlagged {
		t.Errorf("%s-A1: fixture HolderDirect (journal.JournalCore field) not flagged — "+
			"the holder-seal classifier is broken (fixture wiring or scan logic)",
			sagaJournalHolderSealRule)
	}
	if !aliasFlagged {
		t.Errorf("%s-A1: fixture HolderViaAlias (alias to journal.JournalCore) not flagged — "+
			"classifyResolvedJournalType is not calling types.Unalias; on Go 1.23+ an alias "+
			"is *types.Alias, so the alias-typed field evades the seal (F1 regression)",
			sagaJournalHolderSealRule)
	}
}

// TestSagaJournalHolderSeal_A1_HeartbeatFuncFieldFlagged is the F3 regression
// (rule 3): a struct persisting a Heartbeater-shaped func value as a field — the
// func-value equivalent of a journal.Heartbeater field — must be flagged, while
// a non-heartbeat func field must NOT (shape-specific, not "any func"). This
// closes the path where a heartbeat func handed in from outside runtime/saga is
// stashed in a field to drive a centralized loop, which neither the interface
// holder seal nor the name-filtered NO-HEARTBEAT-LOOP A1 catches.
func TestSagaJournalHolderSeal_A1_HeartbeatFuncFieldFlagged(t *testing.T) {
	t.Parallel()

	diags := RunTypedFixture(t, FixtureOpts{},
		[]string{"./tools/archtest/testdata/saga_journal_holder_seal_fixtures/funcfieldholder"},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, file := range p.Files {
				rel := filepath.ToSlash(p.Rel(file))
				EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					for _, field := range st.Fields.List {
						if d, ok := heartbeatFuncFieldDiag(p, rel, ts.Name.Name, field); ok {
							out = append(out, d)
						}
					}
				})
			}
			return out
		})

	var anonFlagged, namedFlagged, plainFlagged bool
	for _, d := range diags {
		switch {
		case strings.Contains(d.Message, "HeartbeatFuncHolder"):
			anonFlagged = true
		case strings.Contains(d.Message, "NamedFuncHolder"):
			namedFlagged = true
		case strings.Contains(d.Message, "PlainFuncHolder"):
			plainFlagged = true
		}
	}
	if !anonFlagged {
		t.Errorf("%s-A1: HeartbeatFuncHolder (anonymous heartbeat-shaped func field) not flagged — "+
			"the func-value evasion path is open (F3 regression)", sagaJournalHolderSealRule)
	}
	if !namedFlagged {
		t.Errorf("%s-A1: NamedFuncHolder (named heartbeat-shaped func type field) not flagged — "+
			"heartbeatFuncFieldDiag must resolve the named type's underlying signature", sagaJournalHolderSealRule)
	}
	if plainFlagged {
		t.Errorf("%s-A1: PlainFuncHolder (non-heartbeat func field) wrongly flagged — "+
			"the shape match is too loose (would false-positive on ordinary func fields)", sagaJournalHolderSealRule)
	}
}
