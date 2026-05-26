// invariants:
//   - INVARIANT: OUTBOX-STATE-TRANSITION-COMPLETENESS-01
//   - INVARIANT: OUTBOX-STATE-LITERAL-BAN-01
//   - INVARIANT: OUTBOX-STATE-TRANSITION-GUARD-01
//
// outbox_state_invariants_test.go — guards that kernel/outbox.State is the
// single source of truth for the outbox entry state machine and its wire/DB
// status strings.
//
//   - COMPLETENESS: every State const is a key in the stateTransitions map or a
//     documented terminal (published/dead), so a new state cannot be added as a
//     silent dead end.
//   - LITERAL-BAN: the adapters/postgres outbox SQL files must not bind bare
//     status string literals — State.String() is the sole sanctioned producer.
//   - TRANSITION-GUARD: the relay's settlement decision points (functions that
//     call MarkPublished/MarkDead/MarkRetry) must also call TransitionState, so
//     the Go state machine actually guards the real runtime transitions rather
//     than being a decorative table.
//
// AI-robust grade: Medium for all three (AST set-difference / scoped BasicLit
// value ban / per-FuncDecl co-location). SQL args are `...any`, so the wire
// string cannot be type-constrained at the binding site — a scoped exact-value
// BasicLit ban is the highest grade achievable for LITERAL-BAN; blind-spot
// lists + reverse self-checks below are the grade justification material.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	stateConstTypeName     = "State"
	stateTransitionsVarN   = "stateTransitions"
	relayMarkSettlementPkg = "runtime/outbox"
)

// outboxStatusLiterals are the wire/DB status strings produced exclusively by
// State.String(). Banned as bare BasicLits in the adapters/postgres outbox SQL
// files (LITERAL-BAN). Column names like "published_at"/"dead_at"/"claimed_at"
// do not exact-match these and are unaffected.
var outboxStatusLiterals = map[string]struct{}{
	"pending": {}, "claiming": {}, "published": {}, "dead": {},
}

// outboxTerminalStates are the State consts that legitimately have no outgoing
// transitions (absent from stateTransitions). Hardcoded by name — blind spot:
// renaming a terminal must update this set. Reverse-checked by the round-trip
// test in kernel/outbox/state_test.go (IsTerminal) and the completeness test.
var outboxTerminalStates = map[string]struct{}{
	"StatePublished": {}, "StateDead": {},
}

func TestOutboxStateTransitionCompleteness(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	diags := Run(t, DirsScope(root, []string{"kernel/outbox"}), func(p *Pass) []Diagnostic {
		declared := map[string]token.Pos{}
		covered := map[string]struct{}{}
		var declFile *ast.File
		for _, f := range p.Files {
			if strings.HasSuffix(p.Rel(f), "_test.go") {
				continue
			}
			for name, pos := range collectConstNamesOfType(f, stateConstTypeName) {
				declared[name] = pos
				declFile = f
			}
			for name := range collectMapKeyIdents(f, stateTransitionsVarN) {
				covered[name] = struct{}{}
			}
		}
		var d []Diagnostic
		for name, pos := range declared {
			if _, ok := covered[name]; ok {
				continue
			}
			if _, term := outboxTerminalStates[name]; term {
				continue
			}
			d = append(d, Diagnostic{
				Rel:  p.Rel(declFile),
				Line: p.Fset.Position(pos).Line,
				Message: "outbox.State const " + name + " is neither a key in stateTransitions nor a " +
					"documented terminal (published/dead); it is a silent dead state",
			})
		}
		return d
	})
	Report(t, "OUTBOX-STATE-TRANSITION-COMPLETENESS-01", diags)
}

func TestOutboxStateLiteralBan(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	diags := Run(t, DirsScope(root, []string{"adapters/postgres"}), func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Scope to the outbox SQL-binding files only; runtime/outbox uses
			// typed kout.State (slog labels there are not status values).
			if strings.HasSuffix(rel, "_test.go") || !strings.Contains(rel, "outbox") {
				continue
			}
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				if lit.Kind != token.STRING {
					return
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					return
				}
				if _, banned := outboxStatusLiterals[val]; banned {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(lit.Pos()).Line,
						Message: "bare outbox status literal " + strconv.Quote(val) +
							" — bind kernel/outbox.State*.String() instead (single source of the wire status string)",
					})
				}
			})
		}
		return d
	})
	Report(t, "OUTBOX-STATE-LITERAL-BAN-01", diags)
}

// outboxSettlementMarks are the store mutations that move an entry out of
// claiming. A function calling any of them must also call TransitionState.
// Blind spots (documented):
//   - TransitionState placed in a different function than the Mark call would
//     evade per-FuncDecl co-location; acceptable since the guard's purpose is to
//     keep the assertion adjacent to the mutation.
//   - The guard is scoped to relay.go — the production settlement loop. A new
//     settlement path added to a different runtime/outbox file would not be
//     covered. outboxtest conformance helpers call the store API directly to
//     exercise it (not to settle in the relay loop) and are intentionally out
//     of scope.
var outboxSettlementMarks = map[string]struct{}{
	"MarkPublished": {}, "MarkDead": {}, "MarkRetry": {},
}

func TestOutboxStateTransitionGuard(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	diags := Run(t, DirsScope(root, []string{relayMarkSettlementPkg}), func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Settlement decisions live in the relay loop (relay.go); the store
			// API and its conformance helpers legitimately call Mark* without a
			// transition assertion.
			if !strings.HasSuffix(rel, "runtime/outbox/relay.go") {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Body == nil {
					return
				}
				marks := callSelectorsIn(fn.Body, outboxSettlementMarks)
				if len(marks) == 0 {
					return
				}
				if !funcCallsSelector(fn.Body, "TransitionState") {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(fn.Pos()).Line,
						Message: "function " + fn.Name.Name + " settles outbox entries (" + strings.Join(marks, ",") +
							") but does not call TransitionState; settlement must assert the claiming→target transition",
					})
				}
			})
		}
		return d
	})
	Report(t, "OUTBOX-STATE-TRANSITION-GUARD-01", diags)
}

// ---------------------------------------------------------------------------
// Shared AST helpers (used by lifecycle_phase_test.go too).
// ---------------------------------------------------------------------------

// collectConstNamesOfType returns const names whose declared (or iota-inherited)
// type ident equals typeName, mapped to their declaration position.
func collectConstNamesOfType(f *ast.File, typeName string) map[string]token.Pos {
	out := map[string]token.Pos{}
	EachInChildren[ast.GenDecl](f, func(gd *ast.GenDecl) {
		if gd.Tok != token.CONST {
			return
		}
		var lastType string // iota continuation: type carried by the first spec
		EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			if vs.Type != nil {
				if id, ok := vs.Type.(*ast.Ident); ok {
					lastType = id.Name
				} else {
					lastType = ""
				}
			}
			if lastType == typeName {
				for _, n := range vs.Names {
					out[n.Name] = n.Pos()
				}
			}
		})
	})
	return out
}

func forEachVarComposite(f *ast.File, varName string, fn func(*ast.CompositeLit)) {
	EachInChildren[ast.GenDecl](f, func(gd *ast.GenDecl) {
		if gd.Tok != token.VAR {
			return
		}
		EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			for i, n := range vs.Names {
				if n.Name == varName && i < len(vs.Values) {
					if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
						fn(cl)
					}
				}
			}
		})
	})
}

// collectCompositeElementIdents collects plain-ident elements of a composite
// (array/slice) literal bound to varName. The composite's Type node (e.g. the
// array element type ident) is nested, not a direct child, so only the literal
// elements are returned.
func collectCompositeElementIdents(f *ast.File, varName string) map[string]struct{} {
	out := map[string]struct{}{}
	forEachVarComposite(f, varName, func(cl *ast.CompositeLit) {
		EachInChildren[ast.Ident](cl, func(id *ast.Ident) {
			out[id.Name] = struct{}{}
		})
	})
	return out
}

// collectMapKeyIdents collects ident keys of a map composite literal bound to
// varName.
func collectMapKeyIdents(f *ast.File, varName string) map[string]struct{} {
	out := map[string]struct{}{}
	forEachVarComposite(f, varName, func(cl *ast.CompositeLit) {
		EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
			if id, ok := kv.Key.(*ast.Ident); ok {
				out[id.Name] = struct{}{}
			}
		})
	})
	return out
}

// missingKeys returns the sorted keys present in declared but absent from
// covered. Core of every completeness invariant in this PR.
func missingKeys(declared, covered map[string]struct{}) []string {
	var out []string
	for k := range declared {
		if _, ok := covered[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// funcCallsSelector reports whether body contains a call whose function is a
// selector with the given method name (e.g. kout.TransitionState).
func funcCallsSelector(body *ast.BlockStmt, sel string) bool {
	found := false
	EachInSubtree[ast.CallExpr](body, func(c *ast.CallExpr) {
		if se, ok := c.Fun.(*ast.SelectorExpr); ok && se.Sel.Name == sel {
			found = true
		}
	})
	return found
}

// callSelectorsIn returns the sorted set of selector method names from `names`
// that body calls.
func callSelectorsIn(body *ast.BlockStmt, names map[string]struct{}) []string {
	hit := map[string]struct{}{}
	EachInSubtree[ast.CallExpr](body, func(c *ast.CallExpr) {
		if se, ok := c.Fun.(*ast.SelectorExpr); ok {
			if _, want := names[se.Sel.Name]; want {
				hit[se.Sel.Name] = struct{}{}
			}
		}
	})
	out := make([]string, 0, len(hit))
	for k := range hit {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestOutboxStateLiteralBan_ReverseSelfCheck proves the banned-literal matcher
// fires on a status word and not on a column name / unrelated string.
func TestOutboxStateLiteralBan_ReverseSelfCheck(t *testing.T) {
	t.Parallel()
	for _, banned := range []string{"pending", "claiming", "published", "dead"} {
		_, ok := outboxStatusLiterals[banned]
		assert.True(t, ok, "%q must be banned", banned)
	}
	for _, allowed := range []string{"published_at", "dead_at", "claimed_at", "pendingish", "retry"} {
		_, ok := outboxStatusLiterals[allowed]
		assert.False(t, ok, "%q must not be banned (column name / unrelated)", allowed)
	}
}

// TestOutboxStateTransitionCompleteness_BlindSpotShape asserts that
// stateTransitions in kernel/outbox is a composite map literal with a
// non-empty key set — a runtime-built map would yield empty keys and silently
// pass the completeness invariant without actually checking anything.
//
// Mirrors TestCellPhaseRankCompleteness_BlindSpotShape in lifecycle_phase_test.go.
func TestOutboxStateTransitionCompleteness_BlindSpotShape(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	_ = Run(t, DirsScope(root, []string{"kernel/outbox"}), func(p *Pass) []Diagnostic {
		var sawMapComposite bool
		for _, f := range p.Files {
			if strings.HasSuffix(p.Rel(f), "_test.go") {
				continue
			}
			// forEachVarComposite fires only when the var is a *ast.CompositeLit;
			// a runtime-built map (make / append) would not fire this callback.
			forEachVarComposite(f, stateTransitionsVarN, func(cl *ast.CompositeLit) {
				keys := collectMapKeyIdents(f, stateTransitionsVarN)
				if len(keys) > 0 {
					sawMapComposite = true
				}
			})
		}
		assert.True(t, sawMapComposite,
			"expected stateTransitions to be a non-empty composite map literal in kernel/outbox "+
				"(blind-spot guard: a runtime-built map would evade key completeness checks)")
		return nil
	})
}

// TestOutboxStateTransitionGuard_ReverseSelfCheck proves funcCallsSelector and
// callSelectorsIn correctly distinguish functions that call TransitionState from
// those that do not. Two tiny Go source snippets are parsed in-memory:
//
//   - bothFunc calls both store.MarkPublished and kout.TransitionState → guard passes.
//   - markOnlyFunc calls only store.MarkPublished → guard would diagnose.
func TestOutboxStateTransitionGuard_ReverseSelfCheck(t *testing.T) {
	t.Parallel()

	const src = `package p
func bothFunc() {
	store.MarkPublished(ctx, id, lease)
	kout.TransitionState(from, to)
}
func markOnlyFunc() {
	store.MarkPublished(ctx, id, lease)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "reverse_self_check.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)

	funcs := map[string]*ast.FuncDecl{}
	EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		funcs[fn.Name.Name] = fn
	})
	require.Contains(t, funcs, "bothFunc", "bothFunc must be parsed")
	require.Contains(t, funcs, "markOnlyFunc", "markOnlyFunc must be parsed")

	both := funcs["bothFunc"]
	markOnly := funcs["markOnlyFunc"]

	// bothFunc: has a settlement mark AND calls TransitionState → no diagnostic.
	bothMarks := callSelectorsIn(both.Body, outboxSettlementMarks)
	assert.NotEmpty(t, bothMarks, "bothFunc must call at least one settlement mark")
	assert.True(t, funcCallsSelector(both.Body, "TransitionState"),
		"bothFunc must call TransitionState")

	// markOnlyFunc: has a settlement mark but does NOT call TransitionState → would diagnose.
	markOnlyMarks := callSelectorsIn(markOnly.Body, outboxSettlementMarks)
	assert.NotEmpty(t, markOnlyMarks, "markOnlyFunc must call at least one settlement mark")
	assert.False(t, funcCallsSelector(markOnly.Body, "TransitionState"),
		"markOnlyFunc must NOT call TransitionState (guard would fire)")
}
