package governance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
)

// TestArchtestInventoryNoIDTruncation guards against a regex defect in
// scripts/audit/list-archtests.sh that truncated multi-segment governance
// rule IDs in docs/audit/archtest-inventory.md.
//
// History: the original alternation used `\b...|CONSISTENCY|...-[A-Z0-9-]+`
// which matched `\bCONSISTENCY-EMIT-01` mid-token inside
// CONTRACT-CONSISTENCY-EMIT-01, producing CONSISTENCY-EMIT-01 in the
// inventory output. Fix in PR-FUNNEL-03 reordered alternation so longer
// compound prefixes (CONTRACT-CONSISTENCY-EMIT / SLICE-CONSISTENCY /
// DOC-NAME) come before their shorter substrings.
//
// This test asserts that every governance rule ID with a compound prefix
// (one or more internal hyphens before the canonical -NN suffix) appears
// verbatim in the inventory file. New compound-prefix rules MUST be added
// here when introduced.
func TestArchtestInventoryNoIDTruncation(t *testing.T) {
	t.Parallel()

	atRisk := []string{
		"CONTRACT-CONSISTENCY-EMIT-01", // truncated to CONSISTENCY-EMIT-01 pre-fix
		"SLICE-CONSISTENCY-01",
		"DOC-NAME-01",
		"WRAPPER-CONTRACTSPEC-IMPORT-01", // archtest cross-ref kept verbatim
		"WRAPPER-NO-PACKAGE-STATE",
		"FMT-CONTRACT-DIR-ID-MATCH-01",
	}

	inventoryPath := filepath.Join("..", "..", "docs", "audit", "archtest-inventory.md")
	data := fileutil.MustReadFile(t, inventoryPath)
	body := string(data)

	for _, id := range atRisk {
		if !strings.Contains(body, id) {
			t.Errorf("inventory missing full rule ID %q — likely truncated by "+
				"scripts/audit/list-archtests.sh regex; check alternation "+
				"orders longer prefixes first.", id)
		}
	}
}

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
	files := loadGovernancePackageFiles(t, fset, ".")

	funcIdx := buildFuncIndex(files)
	roots := collectBFSRoots(funcIdx)

	if len(roots) == 0 {
		t.Fatalf("BFS: no registration roots found; expected (Validator,rules), " +
			"(Validator,strictRules), (DependencyChecker,checks), and Check* " +
			"public methods on Validator")
	}

	actual := runReachabilityBFS(t, fset, files, funcIdx, roots)
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
// Total: 81 IDs across 11 series.
func goldenRuleIDs() []string {
	return []string{
		// ADV — advisory warnings (rules_misc_advisory.go).
		// ADV-02 was retired before PR-FUNNEL-03; the gap is intentional.
		"ADV-01", "ADV-03", "ADV-04", "ADV-05", "ADV-06",

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

		// FMT — format / structural (rules_fmt.go for FMT-01..15, 24, 26..30
		// + strict-mode FMT-16/17/19/A1/C1 + FMT-20..23/25 in
		// rules_misc_strict.go; FMT-19 implementation in rules_misc_advisory.go).
		"FMT-01", "FMT-02", "FMT-03", "FMT-04", "FMT-05",
		"FMT-06", "FMT-07", "FMT-08", "FMT-09", "FMT-10",
		"FMT-11", "FMT-12", "FMT-13", "FMT-14", "FMT-15",
		// FMT-18 deleted in PR-V1-CODEGEN-FULL-MIGRATION W4 (replaced by
		// archtest CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01); gap intentional.
		"FMT-16", "FMT-17", "FMT-19",
		"FMT-20", "FMT-21", "FMT-22", "FMT-23", "FMT-24", "FMT-25",
		"FMT-26", "FMT-27", "FMT-28", "FMT-29", "FMT-30",
		"FMT-A1", "FMT-C1",

		// OUTGUARD — outbox durability (rules_misc_advisory.go)
		"OUTGUARD-01",

		// REF — reference integrity (rules_ref.go for REF-01..11, 13..17;
		// REF-12 was relocated to rules_fmt.go in PR-FUNNEL-03 because it is
		// I/O-flavored — pairs with FMT cluster's disk-format rules).
		"REF-01", "REF-02", "REF-03", "REF-04", "REF-05",
		"REF-06", "REF-07", "REF-08", "REF-09", "REF-10",
		"REF-11", "REF-12", "REF-13", "REF-14", "REF-15",
		"REF-16", "REF-17",

		// SLICE-CONSISTENCY — slice level vs parent cell (rules_misc_advisory.go)
		"SLICE-CONSISTENCY-01",

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

// loadGovernancePackageFiles parses every non-test .go file directly under
// dir into ast.Files sharing fset.
func loadGovernancePackageFiles(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read governance dir %q: %v", dir, err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("no governance .go files parsed in %q", dir)
	}
	return files
}

// scanPackageConstStrings collects package-level `const NAME = "literal"`
// pairs across files. Function-body consts are intentionally ignored — they
// cannot serve as cross-method emission constants.
func scanPackageConstStrings(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					val, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					out[ident.Name] = val
				}
			}
		}
	}
	return out
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
// recvName is the enclosing method's receiver identifier ("v" / "dc"; ""
// for free functions). recvType is its declared receiver type name (e.g.
// "Validator"; "" for free functions).
func walkRule(
	t *testing.T,
	fset *token.FileSet,
	fd *ast.FuncDecl,
	recvName, recvType string,
	funcIdx map[funcKey]*ast.FuncDecl,
	constMap map[string]string,
	inferred map[*ast.CompositeLit]struct{},
	reachable map[string]struct{},
	queue *[]funcKey,
) {
	t.Helper()
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			// Method-value enqueue: <recvName>.<methodName>. The receiver
			// name check stays — guards against accidental enqueue of
			// methods on unrelated types that happen to share a name.
			// Free functions (recvName == "") cannot enqueue same-receiver
			// methods, so they short-circuit here.
			//
			// INVARIANT: BFS does not currently follow `<param>.<method>`
			// inside a free function (would require type-checking the
			// param to resolve the method's receiver type). governance
			// has no free function that takes *Validator/*DependencyChecker
			// and chains into another method; if added, this branch must
			// be extended via go/types or a structural fallback.
			if recvName == "" {
				return true
			}
			id, ok := x.X.(*ast.Ident)
			if !ok || id.Name != recvName {
				return true
			}
			method := x.Sel.Name
			if _, exists := funcIdx[funcKey{recv: recvType, name: method}]; exists {
				*queue = append(*queue, funcKey{recv: recvType, name: method})
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok {
				if _, exists := funcIdx[funcKey{recv: "", name: id.Name}]; exists {
					*queue = append(*queue, funcKey{recv: "", name: id.Name})
				}
				// Free-function callsites do not carry rule IDs as
				// positional args by convention — IDs are only emitted via
				// newResult / newScopedResult or ValidationResult{Code:...}
				// composite literals. The queued function will be walked
				// in a subsequent BFS step, where its body's emissions are
				// collected. If a future helper takes a rule ID as a
				// positional arg (e.g. `emitFinding("FMT-99", ...)`), this
				// branch must be extended to extract from x.Args[0].
				return true
			}
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Emission detection: any selector whose method name is
			// newResult / newScopedResult is treated as a rule emission
			// site, regardless of the receiver expression. Within the
			// governance package these names are uniquely defined on
			// *locator (embedded into Validator and DependencyChecker),
			// so any reachable .newResult / .newScopedResult call is a
			// legitimate emission. Relaxing the receiver check here is
			// what lets BFS see emissions inside free functions that take
			// a *Validator parameter (where the enclosing recvName is "").
			//
			// INVARIANT: production code must keep newResult/newScopedResult
			// method names unique to *locator. Adding a same-named method
			// to an unrelated type would cause BFS to capture foreign
			// emissions; static checking that constraint is impractical
			// here, so rely on PR review when introducing new types in
			// this package.
			if !isResultEmitter(sel.Sel.Name) || len(x.Args) == 0 {
				return true
			}
			id := resolveIDArg(t, fset, x.Args[0], constMap)
			if id != "" {
				reachable[id] = struct{}{}
			}
		case *ast.CompositeLit:
			if !looksLikeValidationResult(x, inferred) {
				return true
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
				id := resolveIDArg(t, fset, kv.Value, constMap)
				if id != "" {
					reachable[id] = struct{}{}
				}
			}
		}
		return true
	})
}

// isResultEmitter reports whether the named locator method emits a
// ValidationResult whose first positional argument is the rule code.
func isResultEmitter(name string) bool {
	return name == "newResult" || name == "newScopedResult"
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
			if !ok {
				return true
			}
			arr, ok := outer.Type.(*ast.ArrayType)
			if !ok {
				return true
			}
			id, ok := arr.Elt.(*ast.Ident)
			if !ok || id.Name != "ValidationResult" {
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
	funcIdx map[funcKey]*ast.FuncDecl,
	roots []funcKey,
) []string {
	t.Helper()

	constMap := scanPackageConstStrings(files)
	inferred := collectInferredVRLits(files)

	reachable := map[string]struct{}{}
	visited := map[funcKey]struct{}{}
	queue := append([]funcKey(nil), roots...)
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if _, seen := visited[key]; seen {
			continue
		}
		visited[key] = struct{}{}
		fd, ok := funcIdx[key]
		if !ok || fd.Body == nil {
			continue
		}
		_, recvName := extractReceiverInfo(fd)
		walkRule(t, fset, fd, recvName, key.recv, funcIdx, constMap, inferred, reachable, &queue)
	}
	return sortedStringKeys(reachable)
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
