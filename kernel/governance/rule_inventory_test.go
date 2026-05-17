package governance

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// TestRuleReachabilityFromRegistrationRoots proves that every rule ID in
// goldenRuleIDs() is reachable from at least one of the four registration
// roots, AND that nothing reachable is missing from goldenRuleIDs().
//
// Roots:
//  1. (*Validator).rules()           — base pipeline (validate.go)
//  2. (*Validator).strictRules()     — strict-only pipeline (rules_misc_strict.go)
//  3. (*DependencyChecker).checks()  — dependency pipeline (depcheck.go)
//  4. (*Validator).Check<X>          — public CI entry points (CH-01..06)
//
// Edges:
//   - <recvName>.<methodName> selector / call → enqueue same-receiver method
//   - freeFunc(...) call → enqueue free function (e.g. docNamingResult)
//   - <recvName>.newResult / newScopedResult call → extract first arg as ID
//   - ValidationResult{Code: ...} composite literal → extract Code value
//
// ID arg resolution is fail-fast: only string literals and package-level
// const idents are accepted. Any other shape triggers t.Fatalf to force
// new emission patterns through PR review (rather than silently slipping
// past governance).
//
// Replaces TestRuleInventoryGolden (the PR-FUNNEL-03 zero-diff temporary
// hardening): BFS reachability is strictly stronger than literal scanning
// because every reachable ID must come from a literal somewhere in the
// reachable code, while literal scanning misses the "defined but never
// registered" case.
//
// INVARIANT: GOVERNANCE-RULE-REACHABILITY-TEST-01
//
// ref: kubernetes/apimachinery pkg/util/validation/field/errors_test.go
// (golden error-code allowlist + AST-based equivalence check).
func TestRuleReachabilityFromRegistrationRoots(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	files, typesInfo, pkg := loadGovernancePackageWithTypes(t, fset, ".")

	funcIdx := buildFuncIndex(files)

	roots := collectBFSRoots(funcIdx)
	if len(roots) == 0 {
		t.Fatalf("BFS: no registration roots found; expected (Validator,rules), " +
			"(Validator,strictRules), (DependencyChecker,checks), and Check* " +
			"public methods on Validator")
	}

	gate := resolveEmitterGate(t, pkg.Scope())
	actual := runReachabilityBFS(t, fset, files, typesInfo, funcIdx, roots, gate)
	golden := goldenRuleIDs()

	if diff := symmetricDiff(golden, actual); len(diff) > 0 {
		t.Fatalf("rule reachability drift detected — BFS reachable IDs from "+
			"the four registration roots disagree with goldenRuleIDs().\n"+
			"To fix: register the missing rule in rules() / strictRules() / "+
			"checks() / a public Check* method, OR update goldenRuleIDs() if "+
			"the new ID is intentional.\nDiff (- only in golden, + only in "+
			"reachable):\n%s",
			strings.Join(diff, "\n"))
	}
}

// goldenRuleIDs returns the pinned set of all rule IDs declared in
// kernel/governance/*.go. Update this list whenever a rule is added /
// renamed / removed.
//
// Total: 86 IDs across 12 series.
func goldenRuleIDs() []string {
	return []string{
		// ADV — advisory warnings (rules_misc_advisory.go).
		// ADV-02 was retired before PR-FUNNEL-03; the gap is intentional.
		"ADV-01", "ADV-03", "ADV-04", "ADV-05", "ADV-06",

		// CONTRACT-ENDPOINT-TEST-MAPPING — active HTTP contract → slice.verify.contract.serve
		// reverse coverage check (rules_contract_test_mapping.go).
		"CONTRACT-ENDPOINT-TEST-MAPPING-01",

		// CH — contract-health (contracthealth.go + rules_http.go)
		"CH-01", "CH-02", "CH-03", "CH-04", "CH-05", "CH-06",

		// CONTRACT-CONSISTENCY-EMIT — http trigger ↔ outbox emit alignment
		// (rules_misc_consistency.go)
		"CONTRACT-CONSISTENCY-EMIT-01",

		// DEP — dependency graph (depcheck.go)
		"DEP-01", "DEP-02", "DEP-03",

		// DOC-NAME — document literal scanning (rules_misc_advisory.go;
		// strict-mode orchestrator is in rules_misc_strict.go)
		"DOC-NAME-01",

		// FMT — format / structural (rules_fmt.go for FMT-01..15, 24, 26..33
		// + strict-mode FMT-16/17/19/A1/C1 + FMT-20..23/25 in
		// rules_misc_strict.go; FMT-19 implementation in rules_misc_advisory.go).
		"FMT-01", "FMT-02", "FMT-03", "FMT-04", "FMT-05",
		"FMT-06", "FMT-07", "FMT-08", "FMT-09", "FMT-10",
		"FMT-11", "FMT-12", "FMT-13", "FMT-14", "FMT-15",
		// FMT-18 deleted in PR-V1-CODEGEN-FULL-MIGRATION W4 (replaced by
		// archtest CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01); gap intentional.
		// FMT-31 (rules_fmt.go) reclaimed the /internal/v1 caller-clients
		// invariant at the YAML governance layer (charter §5.1 L5→L6 carrier
		// migration, replaces tools/archtest/contract_spec_clients_test.go).
		"FMT-16", "FMT-17", "FMT-19",
		"FMT-20", "FMT-21", "FMT-22", "FMT-23", "FMT-24", "FMT-25",
		"FMT-26", "FMT-27", "FMT-28", "FMT-29", "FMT-30", "FMT-31",
		"FMT-32", "FMT-33",
		"FMT-A1", "FMT-C1",

		// JOURNEY — journey lifecycle & cross-file consistency
		// (rules_journey.go). Inverse-direction REF-07 closure +
		// board.state × yaml.lifecycle strong-mapping matrix. AI-rebust
		// Medium; Hard upgrade paths logged in rules_journey.go godoc.
		"JOURNEY-CONTRACT-EXISTENCE-01", "JOURNEY-STATUS-LIFECYCLE-01",

		// OUTGUARD — outbox durability (rules_misc_advisory.go)
		"OUTGUARD-01",

		// REF — reference integrity (rules_ref.go for REF-01..11, 13..17;
		// REF-12 was relocated to rules_fmt.go in PR-FUNNEL-03 because it is
		// I/O-flavored — pairs with FMT cluster's disk-format rules).
		"REF-01", "REF-02", "REF-03", "REF-04", "REF-05",
		"REF-06", "REF-07", "REF-08", "REF-09", "REF-10",
		"REF-11", "REF-12", "REF-13", "REF-14", "REF-15",
		"REF-16", "REF-17",

		// SLICE-CONSISTENCY — slice level vs parent cell + contractUsages role lower bound (rules_misc_advisory.go)
		"SLICE-CONSISTENCY-01",
		"SLICE-CONSISTENCY-02",

		// TOPO — topology (rules_topo.go)
		"TOPO-01", "TOPO-02", "TOPO-03", "TOPO-04", "TOPO-05",
		"TOPO-06", "TOPO-07", "TOPO-08", "TOPO-09",

		// VERIFY — verification closure (rules_verify.go)
		"VERIFY-01", "VERIFY-02", "VERIFY-03",
		"VERIFY-04", "VERIFY-05", "VERIFY-06",
	}
}

// symmetricDiff returns ordered "- a" / "+ b" lines for items present in only
// one side. Inputs must be sorted.
func symmetricDiff(want, got []string) []string {
	wantSet := map[string]struct{}{}
	for _, s := range want {
		wantSet[s] = struct{}{}
	}
	gotSet := map[string]struct{}{}
	for _, s := range got {
		gotSet[s] = struct{}{}
	}
	var diff []string
	for _, s := range want {
		if _, ok := gotSet[s]; !ok {
			diff = append(diff, "- "+s)
		}
	}
	for _, s := range got {
		if _, ok := wantSet[s]; !ok {
			diff = append(diff, "+ "+s)
		}
	}
	return diff
}

// =============================================================================
// BFS reachability helpers
// =============================================================================

// funcKey identifies a top-level function in the governance package.
// recv is the dereferenced receiver type name (e.g. "Validator"); recv == ""
// denotes a free function.
type funcKey struct {
	recv string
	name string
}

// loadGovernancePackageWithTypes loads the governance package using
// packages.Load (module-aware), returning the AST files (sharing fset)
// and the full *types.Info needed for signature-based BFS emission
// detection.
//
// packages.Load is used over types.Config.Check + importer.Default()
// because the latter relies on GOPATH-style $GOROOT/src lookup and fails
// to resolve module-internal imports (kernel/metadata, etc.) in this
// project. packages.Load consults go/build + module-aware resolvers.
//
// dir must contain the governance package (the test's CWD when called
// with "."). The cost (~1-2s first call) is paid once; subsequent test
// runs share Go build cache.
func loadGovernancePackageWithTypes(t *testing.T, fset *token.FileSet, dir string) ([]*ast.File, *types.Info, *types.Package) {
	t.Helper()
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve abs governance dir: %v", err)
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps |
			packages.NeedImports,
		Dir:  absDir,
		Fset: fset,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatalf("load governance package: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatalf("governance package load reported errors")
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 governance package, got %d", len(pkgs))
	}
	pkg := pkgs[0]
	if pkg.TypesInfo == nil {
		t.Fatalf("governance package has nil TypesInfo")
	}
	if pkg.Types == nil {
		t.Fatalf("governance package has nil Types")
	}

	// Filter test files: rule_inventory_test.go and rule_inventory_bfs_test.go
	// must not appear in the BFS sweep (they would self-reference the helpers).
	// packages.Load with no test build tag returns non-test files only, but
	// keep an explicit filter to harden against future flag changes.
	//
	// pkg.Syntax and pkg.GoFiles share a 1:1 index correspondence by
	// packages.Load contract (parsed AST <-> file path); assert it explicitly
	// so a future loader regression that breaks the alignment fails fast
	// rather than silently picking the wrong path for each AST file.
	if len(pkg.Syntax) != len(pkg.GoFiles) {
		t.Fatalf("packages.Load returned Syntax/GoFiles of unequal length (%d vs %d)",
			len(pkg.Syntax), len(pkg.GoFiles))
	}
	var files []*ast.File
	for i, f := range pkg.Syntax {
		path := pkg.GoFiles[i]
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("no governance .go files parsed via governance.Load in %q", absDir)
	}
	return files, pkg.TypesInfo, pkg.Types
}

// emitterGate bundles the three type-system objects the
// signatureMatchesValidationResultEmitter predicate needs. Resolved once
// at BFS start from the loaded package's scope so the predicate body
// itself contains zero string anchors — every comparison goes through
// go/types Identical on pointer-equal type objects.
//
// locatorType pins the SOLE legitimate emitter-holder receiver (see
// emitter_invariant.go for rationale). Method promotion via embedding
// inherits *locator's method set into the outer Validator /
// DependencyChecker types, but their named types remain distinct from
// locator's; types.Identical(recvNamed, locatorType) is structurally
// immune to that promotion path.
type emitterGate struct {
	ruleCodeType         types.Type // governance.RuleCode (or fixture's RuleCode)
	validationResultType types.Type // governance.ValidationResult
	locatorType          types.Type // governance.locator (the sole emitter holder)
}

// resolveEmitterGate populates an emitterGate from the loaded package's
// top-level scope. Fail-fast on any missing identifier — go/types' loader
// boundary has no compile-time type-reference mechanism, but compile-time
// witnesses in emitter_invariant.go ensure rename of any of these three
// names fails build before this lookup ever runs in production. For
// fixture packages, the fixture source must declare matching identifiers
// (it is a closed synthetic package, decoupled from governance renames).
func resolveEmitterGate(t *testing.T, scope *types.Scope) emitterGate {
	t.Helper()
	rcObj := scope.Lookup("RuleCode")
	if rcObj == nil {
		t.Fatalf("emitter gate: scope.Lookup(\"RuleCode\") returned nil")
	}
	vrObj := scope.Lookup("ValidationResult")
	if vrObj == nil {
		t.Fatalf("emitter gate: scope.Lookup(\"ValidationResult\") returned nil")
	}
	locObj := scope.Lookup("locator")
	if locObj == nil {
		t.Fatalf("emitter gate: scope.Lookup(\"locator\") returned nil")
	}
	return emitterGate{
		ruleCodeType:         rcObj.Type(),
		validationResultType: vrObj.Type(),
		locatorType:          locObj.Type(),
	}
}

// scanPackageConstStrings collects package-level `const NAME = "literal"`
// pairs across files. Function-body consts are intentionally ignored — they
// cannot serve as cross-method emission constants.
func scanPackageConstStrings(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			collectConstStringSpecs(decl, out)
		}
	}
	return out
}

// collectConstStringSpecs walks one top-level decl and forwards each
// const ValueSpec to addConstStringSpec.
func collectConstStringSpecs(decl ast.Decl, out map[string]string) {
	gd, ok := decl.(*ast.GenDecl)
	if !ok || gd.Tok != token.CONST {
		return
	}
	for _, spec := range gd.Specs {
		if vs, ok := spec.(*ast.ValueSpec); ok {
			addConstStringSpec(vs, out)
		}
	}
}

// addConstStringSpec records each (name, string-literal value) pair from
// one ValueSpec. Names without paired string-literal values are skipped.
func addConstStringSpec(vs *ast.ValueSpec, out map[string]string) {
	for i, ident := range vs.Names {
		if i >= len(vs.Values) {
			continue
		}
		lit, ok := vs.Values[i].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if val, err := strconv.Unquote(lit.Value); err == nil {
			out[ident.Name] = val
		}
	}
}

// buildFuncIndex maps every top-level func / method declaration in the
// package to its FuncDecl, keyed by (receiver type, function name). Free
// functions use receiver type "".
func buildFuncIndex(files []*ast.File) map[funcKey]*ast.FuncDecl {
	out := map[funcKey]*ast.FuncDecl{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			recvType, _ := extractReceiverInfo(fd)
			out[funcKey{recv: recvType, name: fd.Name.Name}] = fd
		}
	}
	return out
}

// extractReceiverInfo returns the dereferenced receiver type name and the
// receiver identifier name from a FuncDecl. Free functions return ("", "").
func extractReceiverInfo(fd *ast.FuncDecl) (recvType, recvName string) {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return "", ""
	}
	field := fd.Recv.List[0]
	if len(field.Names) > 0 {
		recvName = field.Names[0].Name
	}
	switch typ := field.Type.(type) {
	case *ast.StarExpr:
		if id, ok := typ.X.(*ast.Ident); ok {
			recvType = id.Name
		}
	case *ast.Ident:
		recvType = typ.Name
	}
	return recvType, recvName
}

// collectBFSRoots returns the seed set:
//   - the three fixed registration-list methods (rules, strictRules, checks),
//   - every (*Validator).Check<X> public method (CI-only entry points).
//
// Roots are sorted for deterministic visitation order.
func collectBFSRoots(funcIdx map[funcKey]*ast.FuncDecl) []funcKey {
	fixed := []funcKey{
		{recv: "Validator", name: "rules"},
		{recv: "Validator", name: "strictRules"},
		{recv: "DependencyChecker", name: "checks"},
	}
	var roots []funcKey
	for _, k := range fixed {
		if _, ok := funcIdx[k]; ok {
			roots = append(roots, k)
		}
	}
	for k := range funcIdx {
		if k.recv != "Validator" {
			continue
		}
		if !strings.HasPrefix(k.name, "Check") || len(k.name) < 6 {
			continue
		}
		next := k.name[5]
		if next < 'A' || next > 'Z' {
			continue
		}
		roots = append(roots, k)
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].recv != roots[j].recv {
			return roots[i].recv < roots[j].recv
		}
		return roots[i].name < roots[j].name
	})
	return roots
}

// walkRule performs the BFS step for one node. ast.Inspect walks the
// function body, simultaneously enqueuing newly-discovered methods / free
// functions and collecting rule IDs emitted via newResult, newScopedResult,
// or ValidationResult composite literals.
//
// bfsContext bundles the immutable inputs and mutable state of one BFS
// run so individual visit-helpers don't need to thread eight or more
// parameters through every call. runReachabilityBFS owns the lifecycle:
// build context → call run(roots) → return sorted reachable IDs.
type bfsContext struct {
	t         *testing.T
	fset      *token.FileSet
	typesInfo *types.Info
	funcIdx   map[funcKey]*ast.FuncDecl
	constMap  map[string]string
	inferred  map[*ast.CompositeLit]struct{}
	gate      emitterGate

	reachable map[string]struct{}
	queue     []funcKey
	visited   map[funcKey]struct{}
}

// run drives the BFS loop until queue drains.
func (c *bfsContext) run(roots []funcKey) []string {
	c.t.Helper()
	c.reachable = map[string]struct{}{}
	c.queue = append([]funcKey(nil), roots...)
	c.visited = map[funcKey]struct{}{}

	for len(c.queue) > 0 {
		key := c.queue[0]
		c.queue = c.queue[1:]
		if _, seen := c.visited[key]; seen {
			continue
		}
		c.visited[key] = struct{}{}
		fd, ok := c.funcIdx[key]
		if !ok || fd.Body == nil {
			continue
		}
		_, recvName := extractReceiverInfo(fd)
		c.walkRule(fd, recvName, key.recv)
	}
	return sortedStringKeys(c.reachable)
}

// walkRule visits the body of one BFS node, dispatching SelectorExpr,
// CallExpr, and CompositeLit nodes to dedicated helpers so each branch
// stays under cognitive-complexity limits.
//
// recvName is the enclosing method's receiver identifier ("v" / "dc"; ""
// for free functions). recvType is its declared receiver type name (e.g.
// "Validator"; "" for free functions).
func (c *bfsContext) walkRule(fd *ast.FuncDecl, recvName, recvType string) {
	c.t.Helper()
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			c.enqueueMethodValue(x, recvName, recvType)
		case *ast.CallExpr:
			c.handleCall(x)
		case *ast.CompositeLit:
			c.captureCodeFields(x)
		}
		return true
	})
}

// enqueueMethodValue follows `<recvName>.<methodName>` selectors that
// resolve to known methods on the enclosing receiver type. Free functions
// short-circuit here (recvName == "").
//
// INVARIANT: BFS does not currently follow `<param>.<method>` inside a
// free function — that would require type-checking the param to resolve
// the method's receiver type. governance has no free function that takes
// *Validator / *DependencyChecker and chains into another method; if
// added, this branch must be extended via go/types or a structural
// fallback.
func (c *bfsContext) enqueueMethodValue(x *ast.SelectorExpr, recvName, recvType string) {
	if recvName == "" {
		return
	}
	id, ok := x.X.(*ast.Ident)
	if !ok || id.Name != recvName {
		return
	}
	method := x.Sel.Name
	if _, exists := c.funcIdx[funcKey{recv: recvType, name: method}]; exists {
		c.queue = append(c.queue, funcKey{recv: recvType, name: method})
	}
}

// handleCall enqueues free-function callees and captures rule IDs from
// emission method calls.
//
// Emission detection is signature-based (not name-based): any reachable
// method whose signature matches isValidationResultEmitter — returns
// ValidationResult, takes string at parameter 0, and has a receiver in
// the same package as ValidationResult — is treated as an emitter and
// has x.Args[0] resolved as a rule ID.
//
// This replaces the pre-2026-05-11 design where handleCall matched
// methods by name (`newResult`/`newScopedResult`) and relied on the
// runtime guard assertEmitterMethodsRestrictedToLocator to constrain the
// receiver. Signature-based matching is genuinely type-aware: renaming
// the emitter, defining a same-named method on a foreign receiver, or
// adding a new emitter shape on *locator with the matching signature all
// flow correctly without further changes.
func (c *bfsContext) handleCall(x *ast.CallExpr) {
	if id, ok := x.Fun.(*ast.Ident); ok {
		// Free-function callsite. Enqueue the callee; rule IDs are not
		// carried as positional args at the callsite by convention.
		if _, exists := c.funcIdx[funcKey{recv: "", name: id.Name}]; exists {
			c.queue = append(c.queue, funcKey{recv: "", name: id.Name})
		}
		return
	}
	if len(x.Args) == 0 {
		return
	}
	fn, recvNamed, _, ok := typesutil.ResolveCallee(c.typesInfo, x)
	if !ok {
		return
	}
	sig := fn.Type().(*types.Signature)
	if !signatureMatchesValidationResultEmitter(sig, recvNamed, c.gate) {
		return
	}
	if id := c.resolveID(x.Args[0]); id != "" {
		c.reachable[id] = struct{}{}
	}
}

// signatureMatchesValidationResultEmitter reports whether sig is a method
// with the canonical emitter shape:
//
//	(receiver *locator) (ruleID RuleCode, ...) ValidationResult
//
// All three gates are type-system based via types.Identical on pointer-
// equal *types.Type objects; predicate body contains zero string anchors:
//  1. param 0 type-identical to gate.ruleCodeType
//  2. result 0 type-identical to gate.validationResultType
//  3. recvNamed type-identical to gate.locatorType (the sole legitimate
//     emitter holder)
//
// History: an earlier R2-P1 draft used a sealed marker interface +
// types.Implements as the owner gate. Reviewer F-1 caught that method
// promotion via embedding inherits *locator's marker method into
// *Validator / *DependencyChecker method sets, so types.Implements
// returned true on those outer types too — defeating "only *locator".
// types.Identical on the named type is structurally immune to method
// promotion (named types remain distinct regardless of which methods
// they inherit). See emitter_invariant.go for the full threat model.
func signatureMatchesValidationResultEmitter(sig *types.Signature, recvNamed *types.Named, gate emitterGate) bool {
	// Variadic emitters are not a canonical shape — a format-string
	// emitter like `newResultf(fmt string, args ...any) ValidationResult`
	// has a string at param 0 but x.Args[0] is the format template, not a
	// rule ID. Reject variadic outright; if a future emitter genuinely
	// needs variadic args, extend handleCall's ID-resolution accordingly.
	if sig.Variadic() {
		return false
	}
	if sig.Params().Len() < 1 || sig.Results().Len() != 1 {
		return false
	}
	if !types.Identical(sig.Params().At(0).Type(), gate.ruleCodeType) {
		return false
	}
	if !types.Identical(sig.Results().At(0).Type(), gate.validationResultType) {
		return false
	}
	// Owner gate: receiver must be type-identical to *locator's named
	// type. Promoted emitter methods on outer receivers (Validator,
	// DependencyChecker via embedding) resolve to recvNamed=locator
	// through typeutil.StaticCallee (which follows method declaration,
	// not selector context), so legitimate v.newResult(...) call sites
	// still pass; only directly-declared emitter-shape methods on
	// non-locator receivers are rejected.
	return types.Identical(recvNamed, gate.locatorType)
}

// captureCodeFields walks one CompositeLit and records the rule ID from
// its Code field. Non-ValidationResult composites are skipped via
// looksLikeValidationResult.
func (c *bfsContext) captureCodeFields(x *ast.CompositeLit) {
	if !looksLikeValidationResult(x, c.inferred) {
		return
	}
	for _, el := range x.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Code" {
			continue
		}
		if id := c.resolveID(kv.Value); id != "" {
			c.reachable[id] = struct{}{}
		}
	}
}

// resolveID is a thin wrapper over resolveIDArg using the context's t /
// fset / constMap.
func (c *bfsContext) resolveID(expr ast.Expr) string {
	return resolveIDArg(c.t, c.fset, expr, c.constMap)
}

// looksLikeValidationResult returns true when the composite literal is
// either explicitly typed as ValidationResult / []ValidationResult or its
// Type is nil but a parent-context pre-pass (collectInferredVRLits) has
// confirmed the literal is the inner element of a []ValidationResult /
// [N]ValidationResult outer literal.
//
// Composite literals of unrelated named types (e.g. errcode.Error or a
// future sibling struct with a Code field nested in []Other{{Code:"X"}})
// return false and are skipped, preventing accidental capture of foreign
// Code values into reachable.
//
// INVARIANT: only direct slice / array nesting is recognized by the
// parent-context pre-pass. Map literals (`map[K]ValidationResult{}`),
// pointer-to-slice, or doubly-nested containers are not covered;
// governance currently never uses these patterns. If added, extend
// collectInferredVRLits accordingly.
func looksLikeValidationResult(c *ast.CompositeLit, inferred map[*ast.CompositeLit]struct{}) bool {
	switch typ := c.Type.(type) {
	case nil:
		_, ok := inferred[c]
		return ok
	case *ast.Ident:
		return typ.Name == "ValidationResult"
	case *ast.ArrayType:
		if id, ok := typ.Elt.(*ast.Ident); ok {
			return id.Name == "ValidationResult"
		}
		return false
	default:
		return false
	}
}

// collectInferredVRLits identifies composite literals whose Type is nil
// but which are the direct element of a []ValidationResult /
// [N]ValidationResult outer literal — Go's type inference fills the
// element type from the outer slice/array, but ast.Inspect cannot see
// that parent context after the fact. This pre-pass records those inner
// literals so looksLikeValidationResult can accept them precisely without
// the previous over-permissive "any nil-Type literal" fallback.
//
// See the INVARIANT note on looksLikeValidationResult for unsupported
// container shapes.
func collectInferredVRLits(files []*ast.File) map[*ast.CompositeLit]struct{} {
	inferred := map[*ast.CompositeLit]struct{}{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			outer, ok := n.(*ast.CompositeLit)
			if !ok || !isValidationResultArrayType(outer.Type) {
				return true
			}
			for _, el := range outer.Elts {
				if inner, ok := el.(*ast.CompositeLit); ok && inner.Type == nil {
					inferred[inner] = struct{}{}
				}
			}
			return true
		})
	}
	return inferred
}

// isValidationResultArrayType reports whether expr is `[]ValidationResult`
// or `[N]ValidationResult` — the only outer types whose nil-Type element
// literals are accepted by looksLikeValidationResult.
func isValidationResultArrayType(expr ast.Expr) bool {
	arr, ok := expr.(*ast.ArrayType)
	if !ok {
		return false
	}
	id, ok := arr.Elt.(*ast.Ident)
	return ok && id.Name == "ValidationResult"
}

// runReachabilityBFS walks files starting from roots and returns the
// sorted set of rule IDs found reachable. Both the production reachability
// test and the fixture-driven negative tests share this routine — the
// production test parses the real kernel/governance package, while
// fixture tests (rule_inventory_bfs_test.go) synthesize source strings
// to exercise BFS edge resolution at boundary cases.
func runReachabilityBFS(
	t *testing.T,
	fset *token.FileSet,
	files []*ast.File,
	typesInfo *types.Info,
	funcIdx map[funcKey]*ast.FuncDecl,
	roots []funcKey,
	gate emitterGate,
) []string {
	t.Helper()
	ctx := &bfsContext{
		t:         t,
		fset:      fset,
		typesInfo: typesInfo,
		funcIdx:   funcIdx,
		constMap:  scanPackageConstStrings(files),
		inferred:  collectInferredVRLits(files),
		gate:      gate,
	}
	return ctx.run(roots)
}

// resolveIDArg returns the rule-ID string from a newResult / newScopedResult
// first argument or a ValidationResult.Code field value. Acceptable forms:
//
//  1. *ast.BasicLit (string literal) — strconv.Unquote
//  2. *ast.Ident bound to a package-level const string in constMap
//
// Anything else triggers t.Fatalf, forcing any new emission shape through
// PR review (the alternative — silently skipping — would let new misshapen
// emissions slip past governance).
//
// t.Fatalf terminates the current goroutine via runtime.Goexit; do not
// downgrade to t.Errorf "to collect more errors" — partial reachability
// data is unreliable, and subsequent ID extractions would still feed into
// the comparison set, producing misleading diff output. Fail-fast here is
// the only correct semantics.
func resolveIDArg(
	t *testing.T,
	fset *token.FileSet,
	expr ast.Expr,
	constMap map[string]string,
) string {
	t.Helper()
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			if v, err := strconv.Unquote(e.Value); err == nil {
				return v
			}
		}
	case *ast.Ident:
		if v, ok := constMap[e.Name]; ok {
			return v
		}
	}
	t.Fatalf("BFS: unrecognized rule-ID arg pattern at %s — only string "+
		"literal or package const ident are accepted; refactor the call "+
		"site or extend scanPackageConstStrings to cover the new shape",
		fset.Position(expr.Pos()))
	return ""
}

// sortedStringKeys returns the keys of m in ascending order.
func sortedStringKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
