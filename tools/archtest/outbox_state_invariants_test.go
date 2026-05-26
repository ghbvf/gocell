// invariants:
//   - INVARIANT: OUTBOX-STATE-TRANSITION-COMPLETENESS-01
//   - INVARIANT: OUTBOX-STATE-LITERAL-BAN-01
//   - INVARIANT: OUTBOX-STATE-TRANSITION-GUARD-01
//
// outbox_state_invariants_test.go — guards that kernel/outbox.State is the
// single source of truth for the outbox entry state machine and its wire/DB
// status strings.
//
//   - COMPLETENESS: every State const is a key in the stateTransitions map.
//     Terminal states (StatePublished, StateDead) are explicit keys with empty
//     slices, so a new state cannot be added as a silent dead end.
//   - LITERAL-BAN: the adapters/postgres outbox SQL files must not bind bare
//     status string literals — State.String() is the sole sanctioned producer.
//   - TRANSITION-GUARD: the relay's settlement decision points (functions that
//     call MarkPublished/MarkDead/MarkRetry) must also call Transition, so
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

// containsQuotedStatusLiteral reports whether s contains a single-quoted SQL
// status token (e.g. 'pending', 'dead') as a substring. This catches SQL strings
// like "UPDATE outbox_entries SET status='dead' WHERE ..." that embed the status
// value inline rather than as a standalone Go literal.
//
// Returns the offending token (without quotes) and true when found, or ("", false)
// when the string is clean. Column names like 'published_at' do not match because
// the check requires exact single-quoted tokens against the known status set.
func containsQuotedStatusLiteral(s string) (string, bool) {
	for tok := range outboxStatusLiterals {
		quoted := "'" + tok + "'"
		if strings.Contains(s, quoted) {
			return tok, true
		}
	}
	return "", false
}

// TestOutboxStateTransitionCompleteness verifies that every State const is an
// explicit key in stateTransitions. Terminal states (StatePublished, StateDead)
// are expected to be present with empty slices — the explicit empty-slice form
// documents intent and is machine-verifiable without a separate terminal
// hardcode list.
//
// Blind spots:
//   - collectMapKeyIdents reads composite literal keys from the AST; a
//     runtime-built map (make + store) would yield empty keys and silently pass.
//     TestOutboxStateTransitionCompleteness_BlindSpotShape guards against this.
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
			d = append(d, Diagnostic{
				Rel:  p.Rel(declFile),
				Line: p.Fset.Position(pos).Line,
				Message: "outbox.State const " + name + " is not a key in stateTransitions; " +
					"add it with an empty slice if terminal (StatePublished/StateDead) or " +
					"with its valid targets if non-terminal",
			})
		}
		return d
	})
	Report(t, "OUTBOX-STATE-TRANSITION-COMPLETENESS-01", diags)
}

// TestOutboxStateLiteralBan checks that adapters/postgres outbox files do not
// bind bare status string literals.
//
// Blind spots (documented — true Medium ceiling, not Soft):
//  1. Scope filter `!strings.Contains(rel, "outbox")` excludes adapters/postgres
//     files whose names do not contain "outbox". A file binding status literals
//     under a different name (e.g. a migration helper) would evade this check.
//     Mitigation: migration SQL files do not go through Go string literals; they
//     use parameterised queries via the store layer.
//  2. Tokens 'dead'/'pending' could collide with same-named local concepts in
//     session/command/saga packages. The scope filter (adapters/postgres + outbox
//     filename) narrows this to the outbox store, where these values are
//     exclusively outbox status strings.
//
// These blind spots arise from SQL args being `...any` — the wire string cannot
// be type-constrained at the binding site. This is a true Medium ceiling;
// TestOutboxStateLiteralBan_ReverseSelfCheck verifies the matching logic is
// correct within the covered scope.
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
				// Path A: exact-match — Go string literal whose value IS a status token.
				if _, banned := outboxStatusLiterals[val]; banned {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(lit.Pos()).Line,
						Message: "bare outbox status literal " + strconv.Quote(val) +
							" — bind kernel/outbox.State*.String() instead (single source of the wire status string)",
					})
					return
				}
				// Path B: substring-match — SQL string containing a single-quoted
				// status token like "WHERE status = 'pending'" or
				// "SET status='dead'".  Column names like 'published_at' do not
				// match because containsQuotedStatusLiteral checks exact tokens.
				if tok, found := containsQuotedStatusLiteral(val); found {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(lit.Pos()).Line,
						Message: "SQL string contains bare single-quoted outbox status literal '" + tok + "'" +
							" — use kernel/outbox.State*.String() as the bind parameter instead",
					})
				}
			})
		}
		return d
	})
	Report(t, "OUTBOX-STATE-LITERAL-BAN-01", diags)
}

// outboxSettlementMarks are the store mutations that move an entry out of
// claiming. A function calling any of them must also call Transition.
//
// Blind spots (documented):
//   - Transition placed in a different function than the Mark call would
//     evade per-FuncDecl co-location; acceptable since the guard's purpose is to
//     keep the assertion adjacent to the mutation.
//   - The guard is scoped to relay.go — the production settlement loop. A new
//     settlement path added to a different runtime/outbox file would not be
//     covered. outboxtest conformance helpers call the store API directly to
//     exercise it (not to settle in the relay loop) and are intentionally out
//     of scope.
//
// Intentional scope boundary: this guard covers only relay.go's Go-side
// settlement decisions (claiming→published/dead/pending via Mark*). The
// store's ClaimPending (pending→claiming) and ReclaimStale
// (claiming→pending/dead) have no Mark* pairing to cross-check — their
// correctness is enforced by SQL CAS predicates, conformance behaviour tests,
// and LITERAL-BAN. Adding Transition calls there would provide no
// pairing-check value and would be misleading.
var outboxSettlementMarks = map[string]struct{}{
	"MarkPublished": {}, "MarkDead": {}, "MarkRetry": {},
}

// markToTransitionTarget maps each settlement Mark method to the expected
// Transition second-argument (target state name) that must appear in
// the same function body. A function that calls MarkPublished but passes
// StatePending as the target would be a Go-level logical error; this check
// catches such mismatches at CI time.
//
//   - MarkPublished → StatePublished (claiming entry succeeded)
//   - MarkDead      → StateDead      (retry budget exhausted)
//   - MarkRetry     → StatePending   (transient failure, back to queue)
var markToTransitionTarget = map[string]string{
	"MarkPublished": "StatePublished",
	"MarkDead":      "StateDead",
	"MarkRetry":     "StatePending",
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
				// Check 1: the function must call Transition at all.
				if !funcCallsSelector(fn.Body, "Transition") {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(fn.Pos()).Line,
						Message: "function " + fn.Name.Name + " settles outbox entries (" + strings.Join(marks, ",") +
							") but does not call Transition; settlement must assert the claiming→target transition",
					})
					return
				}
				// Check 2: for each Mark called, verify that the expected
				// Transition target state appears as the second argument of
				// a Transition call in the same function body.
				//
				// We collect all second-arg .Sel.Name values from Transition
				// CallExprs (e.g. kout.Transition(StateClaiming, kout.StatePublished)
				// → "StatePublished"). Each Mark must have its expected target present.
				transitionTargets := map[string]struct{}{}
				EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
					se, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || se.Sel.Name != "Transition" {
						return
					}
					if len(call.Args) < 2 {
						return
					}
					// The second arg is a SelectorExpr like kout.StatePublished.
					argSel, ok := call.Args[1].(*ast.SelectorExpr)
					if !ok {
						return
					}
					transitionTargets[argSel.Sel.Name] = struct{}{}
				})
				for _, markName := range marks {
					expectedTarget, known := markToTransitionTarget[markName]
					if !known {
						continue
					}
					if _, present := transitionTargets[expectedTarget]; !present {
						d = append(d, Diagnostic{
							Rel:  rel,
							Line: p.Fset.Position(fn.Pos()).Line,
							Message: "function " + fn.Name.Name + " calls " + markName +
								" but Transition does not target " + expectedTarget +
								"; the Mark↔target pairing must match (MarkPublished↔StatePublished," +
								" MarkDead↔StateDead, MarkRetry↔StatePending)",
						})
					}
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
// selector with the given method name (e.g. kout.Transition).
//
//nolint:unparam // general helper; all callers currently pass "Transition"
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
// fires on a status word and not on a column name / unrelated string. It also
// exercises the SQL-substring matcher (containsQuotedStatusLiteral).
func TestOutboxStateLiteralBan_ReverseSelfCheck(t *testing.T) {
	t.Parallel()

	// Exact-equal path: all four status tokens must be in the banned set.
	for _, banned := range []string{"pending", "claiming", "published", "dead"} {
		_, ok := outboxStatusLiterals[banned]
		assert.True(t, ok, "%q must be banned", banned)
	}
	// Exact-equal path: column names and unrelated strings must not be banned.
	for _, allowed := range []string{"published_at", "dead_at", "claimed_at", "pendingish", "retry"} {
		_, ok := outboxStatusLiterals[allowed]
		assert.False(t, ok, "%q must not be banned (column name / unrelated)", allowed)
	}

	// SQL-substring path: single-quoted status tokens embedded in SQL strings
	// must be detected.
	sqlHits := []struct {
		input string
		want  string
	}{
		{"UPDATE outbox_entries SET status='dead' WHERE id=$1", "dead"},
		{"SELECT * FROM outbox_entries WHERE status = 'pending' LIMIT 10", "pending"},
		{"SET status='claiming', claimed_at=now()", "claiming"},
		{"WHERE status='published'", "published"},
	}
	for _, tc := range sqlHits {
		tok, found := containsQuotedStatusLiteral(tc.input)
		assert.True(t, found, "expected SQL substring match in %q", tc.input)
		assert.Equal(t, tc.want, tok, "unexpected token in %q", tc.input)
	}

	// SQL-substring path: column names like 'published_at' must NOT match
	// (only exact token 'published' matches, not 'published_at').
	sqlMisses := []string{
		"SELECT published_at FROM outbox_entries",
		"ORDER BY dead_at ASC",
		"WHERE claimed_at < now() - interval '1 hour'",
		"no status here",
		"SELECT * FROM outbox_entries WHERE id=$1",
	}
	for _, s := range sqlMisses {
		tok, found := containsQuotedStatusLiteral(s)
		assert.False(t, found, "expected no SQL substring match in %q (got %q)", s, tok)
	}
}

// TestOutboxStateTransitionCompleteness_BlindSpotShape asserts that
// stateTransitions in kernel/outbox is a composite map literal with a
// non-empty key set — a runtime-built map would yield empty keys and silently
// pass the completeness invariant without actually checking anything.
//
// Mirrors TestCellLifecycleRankCompleteness_BlindSpotShape in lifecycle_phase_test.go.
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
// callSelectorsIn correctly distinguish functions that call Transition from
// those that do not. It also verifies the Mark↔target pairing logic:
//
//   - bothFunc: calls MarkPublished + Transition(_, StatePublished) → passes all checks.
//   - markOnlyFunc: calls only MarkPublished → guard fires (no Transition).
//   - wrongTargetFunc: calls MarkPublished + Transition(_, StatePending) → pairing check fires.
//   - multiMarkFunc: calls MarkDead+MarkRetry + matching targets → passes.
func TestOutboxStateTransitionGuard_ReverseSelfCheck(t *testing.T) {
	t.Parallel()

	const src = `package p
import kout "github.com/ghbvf/gocell/kernel/outbox"

func bothFunc() {
	kout.Transition(kout.StateClaiming, kout.StatePublished)
	store.MarkPublished(ctx, id, lease)
}
func markOnlyFunc() {
	store.MarkPublished(ctx, id, lease)
}
func wrongTargetFunc() {
	kout.Transition(kout.StateClaiming, kout.StatePending)
	store.MarkPublished(ctx, id, lease)
}
func multiMarkFunc() {
	kout.Transition(kout.StateClaiming, kout.StateDead)
	kout.Transition(kout.StateClaiming, kout.StatePending)
	store.MarkDead(ctx, id, lease, 5, "err")
	store.MarkRetry(ctx, id, lease, 1, retryAt, "err")
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
	require.Contains(t, funcs, "wrongTargetFunc", "wrongTargetFunc must be parsed")
	require.Contains(t, funcs, "multiMarkFunc", "multiMarkFunc must be parsed")

	both := funcs["bothFunc"]
	markOnly := funcs["markOnlyFunc"]
	wrongTarget := funcs["wrongTargetFunc"]
	multi := funcs["multiMarkFunc"]

	// bothFunc: has MarkPublished AND Transition(_, StatePublished) → passes.
	bothMarks := callSelectorsIn(both.Body, outboxSettlementMarks)
	assert.NotEmpty(t, bothMarks, "bothFunc must call at least one settlement mark")
	assert.True(t, funcCallsSelector(both.Body, "Transition"),
		"bothFunc must call Transition")
	// Verify pairing: StatePublished target is present.
	bothTargets := collectTransitionTargets(both.Body)
	assert.Contains(t, bothTargets, "StatePublished",
		"bothFunc must have StatePublished as a Transition target")

	// markOnlyFunc: has a settlement mark but does NOT call Transition → would diagnose.
	markOnlyMarks := callSelectorsIn(markOnly.Body, outboxSettlementMarks)
	assert.NotEmpty(t, markOnlyMarks, "markOnlyFunc must call at least one settlement mark")
	assert.False(t, funcCallsSelector(markOnly.Body, "Transition"),
		"markOnlyFunc must NOT call Transition (guard would fire)")

	// wrongTargetFunc: calls MarkPublished + Transition(_, StatePending) → pairing mismatch.
	wrongMarks := callSelectorsIn(wrongTarget.Body, outboxSettlementMarks)
	assert.NotEmpty(t, wrongMarks, "wrongTargetFunc must call MarkPublished")
	assert.True(t, funcCallsSelector(wrongTarget.Body, "Transition"),
		"wrongTargetFunc calls Transition (check 1 passes)")
	wrongTargets := collectTransitionTargets(wrongTarget.Body)
	assert.NotContains(t, wrongTargets, "StatePublished",
		"wrongTargetFunc must NOT have StatePublished target (pairing check would fire)")

	// multiMarkFunc: calls MarkDead+MarkRetry with StateDead+StatePending targets → passes.
	multiMarks := callSelectorsIn(multi.Body, outboxSettlementMarks)
	assert.ElementsMatch(t, []string{"MarkDead", "MarkRetry"}, multiMarks,
		"multiMarkFunc must call MarkDead and MarkRetry")
	multiTargets := collectTransitionTargets(multi.Body)
	assert.Contains(t, multiTargets, "StateDead", "multiMarkFunc must have StateDead target")
	assert.Contains(t, multiTargets, "StatePending", "multiMarkFunc must have StatePending target")
}

// collectTransitionTargets is a test helper that extracts the second-argument
// selector names from all Transition calls in body. Used by
// TestOutboxStateTransitionGuard_ReverseSelfCheck to verify the pairing logic
// without duplicating the production guard's inner loop.
func collectTransitionTargets(body *ast.BlockStmt) map[string]struct{} {
	targets := map[string]struct{}{}
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		se, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || se.Sel.Name != "Transition" {
			return
		}
		if len(call.Args) < 2 {
			return
		}
		argSel, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok {
			return
		}
		targets[argSel.Sel.Name] = struct{}{}
	})
	return targets
}
