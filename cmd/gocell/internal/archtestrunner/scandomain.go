package archtestrunner

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// scandomain.go derives, per archtest rule (test function), the set of source
// files that rule scans, so `gocell verify archtest --changed` can map a
// changed source file to the rules it could affect (gh #1877) instead of only
// re-running rules whose own *_test.go file changed (the #1563 mechanical
// version).
//
// The scan domain is recovered by static AST analysis of the tools/archtest
// package: each rule expresses its scope through a scope constructor
// (Production / Typed / DirsScope / ModuleScope / Fixture / StandaloneModule),
// often inside a companion CheckXxx func reached via Report(t, rule, CheckXxx(...)).
// This is an approximation, not an exact scan trace — when a scope argument is
// computed (a non-literal var, an unresolved helper, a whole-module scope) the
// rule's domain is left UNKNOWN and the rule always runs. --changed is a fast
// pre-filter, never the merge gate (the full sharded run is authoritative), so
// over-running is safe and under-running (a false negative) is the only failure
// that matters — the zero-value semantics below make that unrepresentable.

// fileDomain describes the source-file domain a single archtest rule scans.
//
// SAFETY (Hard, zero-value semantics): the zero value fileDomain{} has
// scoped==false, which domainSelectsChange treats as "unknown => always run".
// Every path that cannot statically determine a rule's scope — a parse failure,
// an unrecognized scope constructor, a computed (non-literal) scope argument, or
// a test func absent from the index (missing map key) — therefore falls back to
// the always-run zero value. Skipping a rule requires actively producing a
// scoped fileDomain whose matchers reject the change; it cannot happen by
// omission. This keeps --changed source-mode free of false negatives.
type fileDomain struct {
	// scoped is false (the zero value) when the rule's scan domain could not be
	// statically determined; such rules always run (conservative).
	scoped bool
	// productionGo is true when the rule scans all non-generated production Go
	// (the Production(...) scope); any production .go change selects it.
	productionGo bool
	// prefixes are repo-relative, slash-separated path prefixes (no leading
	// "./", no trailing "/..." or "/") the rule scans; a changed file at or
	// under any prefix selects the rule.
	prefixes []string
	// defFiles are the repo-relative tools/archtest/*.go files defining the
	// rule's call closure (its own *_test.go + every companion CheckXxx / helper
	// it reaches). A changed file equal to one of these selects the rule —
	// editing a rule's own definition re-runs it. Matched by exact equality
	// (they are files, not dir prefixes).
	defFiles []string
}

// domainSelectsChange reports whether a changed repo-relative file (slash form)
// could affect a rule with the given domain.
//
// Unknown (unscoped) domains always select (safety). A productionGo domain
// selects any non-generated production .go change. A scanned prefix selects a
// change at or under it. A defFile selects a change to the rule's own
// definition (exact path match).
func domainSelectsChange(d fileDomain, changedRepoRelPath string) bool {
	if !d.scoped {
		return true // unknown => always run (no false negatives)
	}
	if d.productionGo && isProductionGoChange(changedRepoRelPath) {
		return true
	}
	for _, p := range d.prefixes {
		if pathHasPrefix(changedRepoRelPath, p) {
			return true
		}
	}
	for _, f := range d.defFiles {
		if changedRepoRelPath == f {
			return true
		}
	}
	return false
}

// isProductionGoChange reports whether a changed path is a production Go file
// that a Production(...) scan would load: a .go file not under any generated/
// or testdata/ segment.
//
// Note: _test.go files are conservatively counted as production Go changes here
// — Production(TypedOpts{Tests:false}) does not load test files, but narrowing
// on the per-call Tests flag is deferred (gh #1877 follow-up); over-running a
// Production rule on a _test.go edit is safe.
func isProductionGoChange(p string) bool {
	if !strings.HasSuffix(p, ".go") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "generated" || seg == "testdata" {
			return false
		}
	}
	return true
}

// pathHasPrefix reports whether changed is at or under the repo-relative prefix
// (segment-boundary match, so "adapters/redis" does not match
// "adapters/rediscluster/...").
func pathHasPrefix(changed, prefix string) bool {
	return changed == prefix || strings.HasPrefix(changed, prefix+"/")
}

// buildFileDomainIndex returns a map from archtest test function name to its
// scan domain, derived from a static analysis of the tools/archtest package.
//
// It parses every top-level *.go in tools/archtest as one package (test files
// and their companion .go files alike — the scope of a Report(t, rule,
// CheckXxx(...)) rule lives in the companion CheckXxx), then for each TestXxx
// func walks its intra-package call closure to recover the scan domain.
//
// A single unparseable file is skipped (its funcs become absent => unknown =>
// always run), never failing the whole index — safety over precision.
func buildFileDomainIndex(workspaceRoot string) (map[string]fileDomain, error) {
	dir := filepath.Join(workspaceRoot, archtestPkgDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]fileDomain{}, nil
		}
		return nil, fmt.Errorf("archtestrunner: read archtest dir for domain index: %w", err)
	}

	ix := &archtestPkgIndex{
		funcs:        map[string]*ast.FuncDecl{},
		funcFile:     map[string]string{},
		stringConsts: map[string]string{},
		scopeCache:   map[string]scopeAccum{},
	}

	// Pass 1: parse all package-archtest files; collect funcs (+ their defining
	// file), string consts, and the top-level test func names.
	var testFuncNames []string
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		if perr != nil || f.Name == nil || f.Name.Name != "archtest" {
			continue // skip unparseable or non-archtest-package files (safety)
		}
		ix.collectDecls(f, filepath.ToSlash(filepath.Join(archtestPkgDir, entry.Name())))
		testFuncNames = append(testFuncNames, extractTestFuncNames(f)...)
	}

	// Pass 2: resolve each test func's domain over the now-complete index.
	out := make(map[string]fileDomain, len(testFuncNames))
	for _, name := range testFuncNames {
		out[name] = ix.funcScope(name, map[string]bool{}).toDomain()
	}
	return out, nil
}

// archtestPkgIndex holds the package-level declarations parsed from
// tools/archtest, plus a memo of per-func scope accumulation.
type archtestPkgIndex struct {
	funcs        map[string]*ast.FuncDecl // funcName -> decl (no receiver)
	funcFile     map[string]string        // funcName -> repo-relative defining file
	stringConsts map[string]string        // const name -> string value (best-effort)
	scopeCache   map[string]scopeAccum    // funcName -> resolved scope (memo)
}

// collectDecls indexes top-level funcs (with their defining file) and string
// consts from one file. repoRelFile is the file's repo-relative slash path.
func (ix *archtestPkgIndex) collectDecls(f *ast.File, repoRelFile string) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				ix.funcs[d.Name.Name] = d
				ix.funcFile[d.Name.Name] = repoRelFile
			}
		case *ast.GenDecl:
			if d.Tok == token.CONST {
				ix.collectStringConsts(d)
			}
		}
	}
}

// collectStringConsts records string-literal const declarations (used to
// resolve const-referenced scope prefixes like PlatformCellsDir).
func (ix *archtestPkgIndex) collectStringConsts(d *ast.GenDecl) {
	for _, spec := range d.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, nm := range vs.Names {
			if i >= len(vs.Values) {
				continue
			}
			if s, ok := stringLitValue(vs.Values[i]); ok {
				ix.stringConsts[nm.Name] = s
			}
		}
	}
}

// scopeAccum accumulates the scope evidence found in a func's call closure.
type scopeAccum struct {
	sawAny        bool // any scope constructor was seen
	sawComputed   bool // a scope arg could not be statically resolved
	hasProduction bool // a Production(...) scope was seen
	prefixes      []string
	// defFiles are the repo-relative tools/archtest/*.go files defining the
	// funcs in this closure (the rule's own *_test.go + every companion CheckXxx
	// / helper it reaches). A change to any of them re-runs the rule — editing a
	// rule's definition is as much a trigger as a change to what it scans.
	defFiles []string
}

func (a *scopeAccum) merge(b scopeAccum) {
	a.sawAny = a.sawAny || b.sawAny
	a.sawComputed = a.sawComputed || b.sawComputed
	a.hasProduction = a.hasProduction || b.hasProduction
	a.prefixes = append(a.prefixes, b.prefixes...)
	a.defFiles = append(a.defFiles, b.defFiles...)
}

// toDomain reduces accumulated evidence to a fileDomain. Any uncertainty
// (computed scope, no scope seen, or a scoped-but-empty result) collapses to
// the zero value (unknown => always run). For a resolvable rule, the domain
// carries both its scanned source-dir prefixes and its defining archtest files
// (defFiles), so a change to either re-runs it.
func (a scopeAccum) toDomain() fileDomain {
	if a.sawComputed || !a.sawAny {
		return fileDomain{}
	}
	prefixes := dedupe(a.prefixes)
	if !a.hasProduction && len(prefixes) == 0 {
		return fileDomain{}
	}
	return fileDomain{
		scoped:       true,
		productionGo: a.hasProduction,
		prefixes:     prefixes,
		defFiles:     dedupe(a.defFiles),
	}
}

// maxScopeDepth bounds the intra-package call-closure walk.
const maxScopeDepth = 8

// funcScope resolves (memoized) the scope evidence for a package-level func by
// walking its body: scope constructors are recorded directly; calls to other
// package funcs are followed (so companion CheckXxx scopes attribute back to
// the dispatching test func). Recursion is bounded by a visited stack (cycles
// contribute nothing) and maxScopeDepth.
func (ix *archtestPkgIndex) funcScope(name string, stack map[string]bool) scopeAccum {
	if cached, ok := ix.scopeCache[name]; ok {
		return cached
	}
	if stack[name] || len(stack) > maxScopeDepth {
		// Cannot continue analysis (cycle / depth cap): mark unknown so the
		// truncated closure contaminates the whole rule to always-run — a
		// resolvable sibling scope must never masquerade as the full domain
		// when part of the closure was not analyzed (no false negatives).
		return scopeAccum{sawAny: true, sawComputed: true}
	}
	fn, ok := ix.funcs[name]
	if !ok || fn.Body == nil {
		return scopeAccum{}
	}
	stack[name] = true
	// Record this func's defining file: a change to it re-runs any rule whose
	// closure reaches it (companion CheckXxx / helper, and the test's own file).
	acc := scopeAccum{defFiles: []string{ix.funcFile[name]}}
	// ast.Inspect runs synchronously, so the shared `stack` map is mutated and
	// read in DFS order across the closure and its recursive funcScope calls.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		cname := calleeName(call.Fun)
		if cname == "" {
			return true
		}
		if isScopeCtor(cname, call) {
			ix.recordScope(cname, call, &acc)
			return true
		}
		if _, isPkgFunc := ix.funcs[cname]; isPkgFunc {
			sub := ix.funcScope(cname, stack)
			acc.merge(sub)
		}
		return true
	})
	delete(stack, name)
	ix.scopeCache[name] = acc
	return acc
}

// scopeCtorArgIndex maps a scope-constructor name to the index of its
// repo-relative patterns/dirs argument. Production has none (it scans all
// production Go); ModuleScope/StandaloneModule are handled as unknown in
// recordScope (their domains do not map to a repo-relative prefix).
var scopeCtorArgIndex = map[string]int{
	"Typed":     1, // Typed(opts, patterns)
	"Fixture":   1, // Fixture(opts, patterns)
	"DirsScope": 1, // DirsScope(root, dirs, ...predicates)
}

// isScopeCtor reports whether a call names a scan-scope constructor.
//
// Production, Typed and Fixture require a composite-literal first arg (their
// *Opts struct), which discriminates them from the unrelated 0-arg p.Typed()
// predicate method. DirsScope/ModuleScope/StandaloneModule have no such
// method-name collision in tools/archtest, but are still gated on a minimum
// arg count (the index recordScope reads must exist) for consistency and
// defense against a future same-named method.
//
// Blind spot (safe): only a scope constructor invoked *directly* is recognized.
// A scope held in a variable and passed indirectly (s := Production(...);
// Run(t, s, nil)) produces no constructor call node here, so the rule falls
// back to the unknown (always-run) domain — over-running, never skipping.
func isScopeCtor(name string, call *ast.CallExpr) bool {
	switch name {
	case "Production", "Typed", "Fixture":
		return len(call.Args) >= 1 && isCompositeLit(call.Args[0])
	case "ModuleScope":
		return len(call.Args) >= 1 // needs the root arg
	case "DirsScope":
		return len(call.Args) >= 2 // needs root + dirs args
	case "StandaloneModule":
		return len(call.Args) >= 3 // needs dir + opts + patterns args
	default:
		return false
	}
}

// recordScope folds one scope-constructor call into the accumulator.
func (ix *archtestPkgIndex) recordScope(name string, call *ast.CallExpr, acc *scopeAccum) {
	acc.sawAny = true
	switch name {
	case "Production":
		acc.hasProduction = true
		return
	case "ModuleScope", "StandaloneModule":
		// ModuleScope scans a whole module; StandaloneModule scans a separate
		// fixture module whose patterns are module-relative (not repo-relative).
		// Neither maps to a repo-relative prefix, so cannot narrow → always run.
		acc.sawComputed = true
		return
	}
	argIdx, ok := scopeCtorArgIndex[name]
	if !ok || argIdx >= len(call.Args) {
		acc.sawComputed = true // unknown ctor shape / missing patterns arg: cannot narrow
		return
	}
	raws, ok := ix.evalStringSlice(call.Args[argIdx], map[string]bool{})
	if !ok {
		acc.sawComputed = true // non-literal patterns (var / unresolved helper): cannot narrow
		return
	}
	for _, raw := range raws {
		p, ok := normalizePattern(raw)
		if !ok {
			acc.sawComputed = true // glob / whole-tree pattern: cannot narrow
			return
		}
		acc.prefixes = append(acc.prefixes, p)
	}
}

// evalStringSlice best-effort resolves an expression to a []string of literal
// values: composite literals, append(...), and calls to package-level helpers
// whose body is a single return of a resolvable slice. Returns ok=false for
// anything it cannot statically resolve (vars, spreads, foreign calls).
func (ix *archtestPkgIndex) evalStringSlice(expr ast.Expr, stack map[string]bool) ([]string, bool) {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		out := make([]string, 0, len(e.Elts))
		for _, elt := range e.Elts {
			s, ok := ix.evalStringExpr(elt)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case *ast.CallExpr:
		cname := calleeName(e.Fun)
		if cname == "append" {
			return ix.evalAppend(e, stack)
		}
		fd, ok := ix.funcs[cname]
		if !ok || stack[cname] {
			return nil, false
		}
		stack[cname] = true
		res, ok := ix.evalFuncReturnSlice(fd, stack)
		delete(stack, cname)
		return res, ok
	default:
		return nil, false
	}
}

// evalAppend resolves append(slice, "lit", ...). Variadic spread (append(a,
// b...)) is not resolvable and returns ok=false.
func (ix *archtestPkgIndex) evalAppend(call *ast.CallExpr, stack map[string]bool) ([]string, bool) {
	if len(call.Args) == 0 || call.Ellipsis != token.NoPos {
		return nil, false
	}
	base, ok := ix.evalStringSlice(call.Args[0], stack)
	if !ok {
		return nil, false
	}
	out := append([]string{}, base...)
	for _, a := range call.Args[1:] {
		s, ok := ix.evalStringExpr(a)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// evalFuncReturnSlice resolves a helper whose body returns exactly one slice
// expression (e.g. `return []string{...}` or `return append(base(), "x")`).
// Functions with branching / multiple returns are not resolvable.
func (ix *archtestPkgIndex) evalFuncReturnSlice(fd *ast.FuncDecl, stack map[string]bool) ([]string, bool) {
	if fd.Body == nil {
		return nil, false
	}
	var ret *ast.ReturnStmt
	for _, stmt := range fd.Body.List {
		r, ok := stmt.(*ast.ReturnStmt)
		if !ok {
			continue
		}
		if ret != nil {
			return nil, false // multiple returns: cannot resolve a single value
		}
		ret = r
	}
	if ret == nil || len(ret.Results) != 1 {
		return nil, false
	}
	return ix.evalStringSlice(ret.Results[0], stack)
}

// evalStringExpr resolves an expression to a single string: a string literal or
// a package-level string const reference.
func (ix *archtestPkgIndex) evalStringExpr(expr ast.Expr) (string, bool) {
	if s, ok := stringLitValue(expr); ok {
		return s, true
	}
	if id, ok := expr.(*ast.Ident); ok {
		if v, ok := ix.stringConsts[id.Name]; ok {
			return v, true
		}
	}
	return "", false
}

// normalizePattern reduces a scope pattern to a repo-relative prefix: it strips
// a leading "./", a trailing "/..." and "/", and rejects (ok=false) any pattern
// that cannot be narrowed to a clean path prefix — whole-tree ("..."), glob
// (*?[), or a malformed empty/dot/double-slash segment. Rejecting (rather than
// keeping a malformed prefix) routes the rule to the unknown/always-run domain,
// avoiding a never-matching prefix that would be a silent false negative.
func normalizePattern(raw string) (string, bool) {
	p := strings.TrimSpace(raw)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimSuffix(p, "/...")
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "." || strings.Contains(p, "...") || strings.Contains(p, "//") {
		return "", false
	}
	if strings.ContainsAny(p, "*?[") {
		return "", false
	}
	return p, true
}

// calleeName returns the bare identifier of a call's function — for a plain
// Ident (Production), a selector's final segment (scanner.DirsScope ->
// DirsScope, p.Typed -> Typed). Returns "" for any other callee shape.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	default:
		return ""
	}
}

// isCompositeLit reports whether expr is a composite literal (e.g. TypedOpts{}).
func isCompositeLit(expr ast.Expr) bool {
	_, ok := expr.(*ast.CompositeLit)
	return ok
}

// stringLitValue returns the unquoted value of a string literal expression.
func stringLitValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}
