// INVARIANT: CLI-UNIMPL-HIDE-01
//   - INVARIANT: CLI-TOPLEVEL-HELP-REGISTRY-01
//
// No `gocell` command — at any level — may be visible in help while being
// unimplemented. The five help-bearing verb trees
// (generate / verify / scaffold / check / export) AND the top-level command
// set each own a single typed registry (cmd/gocell/app/subcommand.go's
// subcommand[H]); dispatch and help BOTH derive from that one slice, so a
// command cannot appear in one truth and not the other.
//
// This archtest makes that single-source structurally enforced rather
// than convention. It binds four structural facts in cmd/gocell/app
// production code:
//
//   - Upstream Hard (form-unique): the dispatch functions — the five
//     verb-tree dispatchers (runGenerate, runVerify, runScaffoldWithRoot,
//     runCheck, runExport) plus the top-level Dispatch — contain NO `switch`
//     statement AND resolve the handler via `h, ok := findSub(<registry>, …)`
//     over the dispatcher's own registry AND actually invoke that resolved
//     `h`. This defeats not only a string-literal `case "name":` ladder but
//     also a dummy findSub call paired with a real name→handler map-index /
//     if-ladder dispatch (the resolved handler must be the one called). The
//     expected registry per dispatcher is pinned in dispatchFuncs, so
//     findSub over a parallel/wrong slice is rejected too.
//   - Downstream Hard (verb trees): no `helpEntry{…}` composite literal
//     anywhere in production code carries a string-literal `name`. The only
//     path from a registry to a helpEntry is buildHelpEntries (via
//     renderSubHelp / renderTopHelp), which sets `name: s.name` (a
//     selector, never a literal). A hand-written help list — the other half
//     of the old drift — fails CI.
//   - Downstream Hard (top level): PrintUsage's body is exactly one
//     statement — a renderTopHelp(commands, …) delegation. Any other shape
//     (extra statements, a different callee, a direct fmt.Print* /
//     fmt.Fprintln(os.Stdout,…) / os.Stdout write / hand-printed prose)
//     fails CI. This is form-uniqueness, not an fmt.Print* blacklist, so
//     there is no "like-but-not" gray zone for hand-rolled top-level usage.
//   - No placeholder: production code contains no reachable
//     "not implemented" string return. An unimplemented type is absent
//     from its registry and falls through to the unknown-type error,
//     exactly like a typo.
//
// AI-robust (CLI-TOPLEVEL-HELP-REGISTRY-01, funnel 双向锁): the single
// `commands` package var (dispatch.go) is the funnel; Dispatch and
// PrintUsage both read only it, with no parallel hand-maintained list.
//   - Downstream = Hard (form-unique funnel): help names can only come from
//     buildHelpEntries' `name: s.name` selector; scanHelpEntryNoLiteralName
//     bans literal-name helpEntry package-wide; PrintUsage's body is
//     form-locked to the sole renderTopHelp(commands) delegation. A
//     non-registry command in top-level help is thus inexpressible at the
//     archtest form layer, with no bypass gray zone. Enforcement is
//     archtest-bound (the Go ceiling for "what a func prints"); form
//     uniqueness is the highest grade reachable for this rule shape (charter
//     §Hard 范本 "typed marker funnel for unbounded ops").
//   - Upstream = Hard (form-unique), SHARED with the verb trees:
//     Dispatch ∈ dispatchFuncs → no-switch AND `h,ok := findSub(commands,…)`
//     AND `h` is invoked. The form-uniqueness (handler bound by findSub over
//     the pinned registry, then that exact handler dispatched) closes the
//     weaker "findSub appears somewhere" shape: a dummy findSub paired with a
//     name→handler map-index/if-ladder dispatch fails, as does findSub over a
//     wrong/parallel slice. Enforcement is archtest-AST (Go cannot type-force
//     control flow), so form-uniqueness — no "like-but-not" gray zone — is
//     the highest grade reachable for this rule shape (charter §Hard 范本
//     "typed marker funnel for unbounded ops"); identical mechanism and
//     ceiling for all six dispatchers, graded uniformly (no fork).
//
// Reverse-fixture self-checks below prove each detector actually
// fires (a detector that silently passes everything would itself be the
// bypass), and that the compliant top-level shape is NOT flagged.
//
// # Tool / blind-spot ledger (charter §载体决策原则)
//
// Detection is pure-AST (archtest.Run, no go/types) because the rule's
// truth is structural (presence of a SwitchStmt / a literal-name
// helpEntry / a "not implemented" literal / PrintUsage body shape), not
// type-resolution. AST forms outside the chosen matchers, each with a
// reverse self-check:
//
//  1. Top-level PrintUsage ↔ commands (CLI-TOPLEVEL-HELP-REGISTRY-01) —
//     formerly a declared blind spot (free-form prose compensated by a
//     stale-token guard), now closed: the seven top-level commands live in
//     the `commands` []subcommand registry, Dispatch resolves via findSub
//     (covered by scanDispatchSwitchFree, Dispatch ∈ dispatchFuncs) and
//     PrintUsage renders via a sole renderTopHelp(commands) delegation
//     (scanPrintUsageDerived, which pins both the statement shape AND arg0 ==
//     the canonical `commands` identifier). The old
//     assertPrintUsageNoStaleToken compensation is deleted — a stale token
//     cannot survive in prose that no longer exists. Fixtures:
//     printusage_handwritten (hand-printed prose — must flag) /
//     printusage_wrongvar (sole renderTopHelp but a non-commands registry —
//     must flag) / printusage_delegating (must NOT flag).
//  2. helpEntry built with a named field (`helpEntry{name: …}`) vs
//     positional (`helpEntry{…}`) — both handled (KeyValueExpr key and
//     positional element 0). Fixture: namedfield.
//  3. A "not implemented" produced via a helper call rather than a
//     literal return — the registry shape has no such path; fixture
//     `placeholder` asserts the literal form is caught, and the absence
//     of any indirection is guaranteed by the no-switch + findSub facts
//     (an unregistered type cannot reach a handler at all).
//  4. export is now a registry-backed verb tree, not an excluded alias
//     switch: runExport resolves catalog/metadata via
//     findSub(exportSubcommands, …) and derives `export -h` via renderSubHelp,
//     so it is in dispatchFuncs like the other four. (Previously it was an
//     OUT-of-scope plain alias switch with no helpEntry surface; once it grew
//     a `-h` help surface that exclusion no longer held, so it was folded into
//     the funnel rather than left as a hand-written second source.) There is
//     no remaining excluded dispatcher.
//  5. scanPrintUsageDerived keys on the FuncDecl name "PrintUsage" (a name
//     convention for the single sanctioned entry point) but its match is
//     form-complete: it accepts ONLY the sole-renderTopHelp(commands) body
//     and rejects every other statement form, so unlike a fmt.Print*
//     blacklist there is no fmt.Fprintln / os.Stdout / helper-indirection
//     escape. Two reverse fixtures cover the distinct escapes a weaker
//     matcher would miss: printusage_handwritten (multi-statement, incl. the
//     fmt.Fprintln(os.Stdout,…) form) and printusage_wrongvar (right shape,
//     wrong registry arg — proves the arg0 == `commands` pin, i.e. help
//     cannot be sourced from a second/parallel slice).
//
// ref: docs/plans/202605121830-038-p0-p1-blocking-implementation-plan.md PR-3
// ref: cmd/gocell/app/subcommand.go (the funnel this test guards)
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// dispatchFuncs maps each registry-backed dispatcher to the registry-slice
// identifier it MUST resolve handlers from: the five help-bearing verb-tree
// dispatchers plus the top-level Dispatch (CLI-TOPLEVEL-HELP-REGISTRY-01).
// scanDispatchViaRegistry pins the form `h, ok := findSub(<registry>, …)`
// followed by an invocation of `h` — so dispatch is a registry lookup whose
// resolved handler is the one called, never a switch ladder nor a dummy
// findSub paired with a parallel map-index/if-ladder dispatch.
var dispatchFuncs = map[string]string{
	"runGenerate":         "generateSubcommands",
	"runVerify":           "verifySubcommands",
	"runScaffoldWithRoot": "scaffoldSubcommands",
	"runCheck":            "checkSubcommands",
	"runExport":           "exportSubcommands",
	"Dispatch":            "commands",
}

// TestCLIUnimplHide01 binds the structural single-source facts in
// cmd/gocell/app production code.
func TestCLIUnimplHide01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	diags := Run(t, DirsScope(root, []string{"cmd/gocell/app"}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue // production-only invariant
				}
				d = append(d, scanDispatchViaRegistry(p, f, rel)...)
				d = append(d, scanHelpEntryNoLiteralName(p, f, rel)...)
				d = append(d, scanNoNotImplementedLiteral(p, f, rel)...)
			}
			return d
		})
	Report(t, "CLI-UNIMPL-HIDE-01", diags)
}

// scanDispatchViaRegistry enforces the upstream form-uniqueness fact: each
// dispatcher has zero SwitchStmt, binds its handler via
// `h, ok := findSub(<its registry>, …)`, and invokes that exact `h`. The
// findSub-result-must-be-invoked rule is what defeats a dummy findSub call
// paired with a real map-index/if-ladder dispatch (the weaker "findSub
// appears somewhere" check could not). The registry identifier is pinned per
// dispatcher (dispatchFuncs), so resolving over a parallel/wrong slice fails.
func scanDispatchViaRegistry(p *Pass, f *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Body == nil {
			return
		}
		reg, ok := dispatchFuncs[fn.Name.Name]
		if !ok {
			return
		}
		line := p.Fset.Position(fn.Pos()).Line
		var hasSwitch bool
		EachInSubtree[ast.SwitchStmt](fn.Body, func(*ast.SwitchStmt) { hasSwitch = true })
		if hasSwitch {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fn.Name.Name + " dispatches via switch; a registry-backed " +
					"dispatcher must resolve the handler through findSub so help and " +
					"dispatch cannot drift",
			})
		}
		handler := findSubHandlerIdent(fn.Body, reg)
		if handler == "" {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fn.Name.Name + " does not resolve the handler via findSub(" +
					reg + ", …); dispatch must look the handler up in its registry, " +
					"not a switch/map/if-ladder",
			})
			return // no handler binding to verify invocation against
		}
		if !identIsCalled(fn.Body, handler) {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fn.Name.Name + " resolves a handler via findSub but never " +
					"dispatches it; the findSub result must be the invoked handler " +
					"(guards a dummy findSub paired with a parallel map-index dispatch)",
			})
		}
	})
	return d
}

// findSubHandlerIdent returns the name of the handler variable bound by an
// assignment `<h>, <ok> := findSub(<reg>, …)` whose first argument is the
// identifier regName. Returns "" when no such assignment exists.
func findSubHandlerIdent(body *ast.BlockStmt, regName string) string {
	var name string
	EachInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) {
		if name != "" || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
			return
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return
		}
		fun, ok := call.Fun.(*ast.Ident)
		if !ok || fun.Name != "findSub" || len(call.Args) < 1 {
			return
		}
		arg0, ok := call.Args[0].(*ast.Ident)
		if !ok || arg0.Name != regName {
			return
		}
		if h, ok := as.Lhs[0].(*ast.Ident); ok {
			name = h.Name
		}
	})
	return name
}

// identIsCalled reports whether the identifier name is invoked as `name(…)`
// anywhere in body.
func identIsCalled(body *ast.BlockStmt, name string) bool {
	called := false
	EachInSubtree[ast.CallExpr](body, func(c *ast.CallExpr) {
		if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
			called = true
		}
	})
	return called
}

// scanHelpEntryNoLiteralName enforces the downstream-Hard fact: no
// helpEntry composite literal carries a string-literal name. renderSubHelp
// builds helpEntry{name: s.name, …} from the registry — a literal name is
// a hand-written help list (the deleted printXxxHelp shape).
func scanHelpEntryNoLiteralName(p *Pass, f *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	flag := func(cl *ast.CompositeLit) {
		v := helpEntryNameValue(cl)
		lit, ok := v.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		d = append(d, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(cl.Pos()).Line,
			Message: "hand-written helpEntry with string-literal name " +
				lit.Value + "; help must derive from a subcommand registry " +
				"via renderSubHelp (name: s.name), never a literal list",
		})
	}
	EachInSubtree[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
		switch {
		case isHelpEntryIdent(cl.Type):
			// Explicit `helpEntry{…}` (e.g. renderSubHelp's per-element
			// construction — legitimate when name is a selector).
			flag(cl)
		case isHelpEntrySliceOrArray(cl.Type):
			// `[]helpEntry{ {…}, {…} }` — inner elements carry an elided
			// type (cl.Type == nil), so inspect each direct child.
			EachInChildren[ast.CompositeLit](cl, flag)
		}
	})
	return d
}

// isHelpEntrySliceOrArray reports whether e is `[]helpEntry` or
// `[N]helpEntry` — the container whose elements elide their type.
func isHelpEntrySliceOrArray(e ast.Expr) bool {
	at, ok := e.(*ast.ArrayType)
	return ok && isHelpEntryIdent(at.Elt)
}

// scanNoNotImplementedLiteral enforces: no production string literal
// announces an unimplemented sub-command. The registry shape expresses
// "unimplemented" as absence, not a placeholder branch.
func scanNoNotImplementedLiteral(p *Pass, f *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING {
			return
		}
		if strings.Contains(strings.ToLower(lit.Value), "not implemented") {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(lit.Pos()).Line,
				Message: "string literal " + lit.Value + " announces an " +
					"unimplemented sub-command; an unimplemented type must be " +
					"absent from its registry, not a placeholder branch",
			})
		}
	})
	return d
}

func isHelpEntryIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "helpEntry"
}

// helpEntryNameValue returns the AST value assigned to the helpEntry "name"
// field, handling both `helpEntry{name: X, …}` (KeyValueExpr) and
// `helpEntry{X, …}` (positional, name is field 0).
func helpEntryNameValue(cl *ast.CompositeLit) ast.Expr {
	if len(cl.Elts) == 0 {
		return nil
	}
	if _, keyed := cl.Elts[0].(*ast.KeyValueExpr); !keyed {
		return cl.Elts[0] // positional: name is the first field
	}
	var nameVal ast.Expr
	EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "name" {
			nameVal = kv.Value
		}
	})
	return nameVal
}

// --- Reverse self-checks: prove each detector actually fires ----------------
//
// A detector that silently passes everything would itself be the bypass.
// Each fixture under testdata/cli_unimpl_hide/ contains exactly one
// deliberate violation; the matching detector must flag it.

func parseFixture(t *testing.T, name string) (*Pass, *ast.File, string) {
	t.Helper()
	rel := filepath.Join("testdata", "cli_unimpl_hide", name)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return &Pass{Fset: fset, Files: []*ast.File{f}}, f, rel
}

func TestCLIUnimplHide01_DetectsSwitchDispatch(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "switch_dispatch.go")
	got := scanDispatchViaRegistry(p, f, rel)
	// The fixture's runGenerate both has a switch AND never resolves via
	// findSub(generateSubcommands, …), so the detector must emit exactly two
	// distinct diagnostics. Asserting only "len > 0" would let the detector
	// silently regress to reporting a single condition.
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 diagnostics (switch present + findSub "+
			"resolution absent), got %d: %v", len(got), got)
	}
	if !containsMsg(got, "dispatches via switch") {
		t.Errorf("missing the switch-present diagnostic; got %v", got)
	}
	if !containsMsg(got, "does not resolve the handler via findSub") {
		t.Errorf("missing the findSub-resolution-absent diagnostic; got %v", got)
	}
}

// TestCLIUnimplHide01_DetectsDummyFindSubMapIndex proves the form-unique
// upstream check catches the bypass the old "findSub appears somewhere" shape
// could not: a dispatcher that calls findSub(<registry>, …) but discards the
// result and dispatches via a name→handler map index instead.
func TestCLIUnimplHide01_DetectsDummyFindSubMapIndex(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "dummy_findsub_mapindex.go")
	got := scanDispatchViaRegistry(p, f, rel)
	if len(got) == 0 {
		t.Fatal("expected dummy-findSub + map-index dispatch fixture to be flagged, got 0")
	}
	if !containsMsg(got, "never dispatches it") {
		t.Errorf("missing the findSub-result-not-invoked diagnostic; got %v", got)
	}
}

func TestCLIUnimplHide01_DetectsLiteralHelpEntry(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "literal_helpentry.go")
	got := scanHelpEntryNoLiteralName(p, f, rel)
	if len(got) == 0 {
		t.Fatal("expected literal-name helpEntry fixture to be flagged, got 0")
	}
	// Named-field form (blind-spot ledger §2) must also be caught.
	if !containsMsg(got, "namedKey") || !containsMsg(got, "positional0") {
		t.Errorf("both named-field and positional helpEntry forms must be flagged; got %v", got)
	}
}

func TestCLIUnimplHide01_DetectsNotImplementedLiteral(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "placeholder.go")
	if got := scanNoNotImplementedLiteral(p, f, rel); len(got) == 0 {
		t.Fatal("expected 'not implemented' literal fixture to be flagged, got 0")
	}
}

func containsMsg(diags []Diagnostic, sub string) bool {
	for _, d := range diags {
		if strings.Contains(d.Message, sub) {
			return true
		}
	}
	return false
}

// scanPrintUsageDerived enforces CLI-TOPLEVEL-HELP-REGISTRY-01's downstream
// form-uniqueness: PrintUsage must render the top-level command list by
// delegating to renderTopHelp over the `commands` registry, and do nothing
// else. Its body must be exactly one statement —
// renderTopHelp(commands, …). Any other shape (extra statements, a
// different callee, a direct fmt.Print* / fmt.Fprintln(os.Stdout,…) /
// os.Stdout write / hand-printed prose) is the drift-prone hand-written
// usage form that let stale sub-types resurface in top-level help. Form
// uniqueness — not an fmt.Print* blacklist — leaves no "like-but-not" gray
// zone (e.g. fmt.Fprintln(os.Stdout, …) would silently slip past a
// blacklist but is an extra/foreign statement here).
func scanPrintUsageDerived(p *Pass, f *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Name.Name != "PrintUsage" || fn.Body == nil {
			return
		}
		if isSoleRenderTopHelpCall(fn.Body) {
			return
		}
		d = append(d, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(fn.Pos()).Line,
			Message: "PrintUsage must delegate to renderTopHelp(commands, …) " +
				"as its sole statement; the top-level command list must derive " +
				"from the commands registry, never hand-printed prose " +
				"(CLI-TOPLEVEL-HELP-REGISTRY-01)",
		})
	})
	return d
}

// isSoleRenderTopHelpCall reports whether body is exactly one ExprStmt
// calling renderTopHelp with `commands` as its first argument.
func isSoleRenderTopHelpCall(body *ast.BlockStmt) bool {
	if len(body.List) != 1 {
		return false
	}
	es, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || fun.Name != "renderTopHelp" {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}
	arg0, ok := call.Args[0].(*ast.Ident)
	return ok && arg0.Name == "commands"
}

// TestCLITopLevelHelpRegistry01 binds the top-level downstream form-
// uniqueness fact: PrintUsage derives the command list from the `commands`
// registry via a sole renderTopHelp delegation. Combined with
// Dispatch ∈ dispatchFuncs (scanDispatchSwitchFree, upstream) and the
// package-wide no-literal-helpEntry ban (scanHelpEntryNoLiteralName), the
// top-level commands ↔ PrintUsage prose can no longer drift.
func TestCLITopLevelHelpRegistry01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	diags := Run(t, DirsScope(root, []string{"cmd/gocell/app"}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue // production-only invariant
				}
				d = append(d, scanPrintUsageDerived(p, f, rel)...)
			}
			return d
		})
	Report(t, "CLI-TOPLEVEL-HELP-REGISTRY-01", diags)
}

// TestCLITopLevelHelpRegistry01_DetectsHandwrittenPrintUsage proves
// scanPrintUsageDerived fires on a hand-written, multi-statement PrintUsage
// (including the fmt.Fprintln(os.Stdout, …) form an fmt.Print* blacklist
// would miss). A detector that passed everything would itself be the
// bypass.
func TestCLITopLevelHelpRegistry01_DetectsHandwrittenPrintUsage(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "printusage_handwritten.go")
	got := scanPrintUsageDerived(p, f, rel)
	if len(got) == 0 {
		t.Fatal("expected hand-written PrintUsage fixture to be flagged, got 0")
	}
	if !containsMsg(got, "must delegate to renderTopHelp") {
		t.Errorf("missing the form-uniqueness diagnostic; got %v", got)
	}
}

// TestCLITopLevelHelpRegistry01_DetectsWrongRegistryArg proves the funnel
// rejects a sole renderTopHelp call that renders from a non-`commands`
// registry (a parallel/local source). isSoleRenderTopHelpCall pins arg0 to
// the `commands` identifier; without this assertion the "single source"
// claim would be unproven (the right statement shape over the wrong slice
// would silently pass).
func TestCLITopLevelHelpRegistry01_DetectsWrongRegistryArg(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "printusage_wrongvar.go")
	got := scanPrintUsageDerived(p, f, rel)
	if len(got) == 0 {
		t.Fatal("expected renderTopHelp(non-commands) fixture to be flagged, got 0")
	}
	if !containsMsg(got, "must delegate to renderTopHelp") {
		t.Errorf("missing the form-uniqueness diagnostic; got %v", got)
	}
}

// TestCLITopLevelHelpRegistry01_AcceptsDelegatingPrintUsage proves the
// detector does NOT over-fire: the compliant sole-renderTopHelp(commands)
// shape must produce zero diagnostics, so the rule cannot regress to
// flagging everything.
func TestCLITopLevelHelpRegistry01_AcceptsDelegatingPrintUsage(t *testing.T) {
	t.Parallel()
	p, f, rel := parseFixture(t, "printusage_delegating.go")
	if got := scanPrintUsageDerived(p, f, rel); len(got) != 0 {
		t.Fatalf("compliant sole-delegation PrintUsage must not be flagged, got %v", got)
	}
}
