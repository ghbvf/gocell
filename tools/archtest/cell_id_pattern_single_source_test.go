// invariants asserted in this file:
//   - INVARIANT: CELL-ID-PATTERN-SINGLE-SOURCE-01
//
// CELL-ID-PATTERN-SINGLE-SOURCE-01: every .go file in the module that
// contains a string literal (BasicLit STRING node, or a regexp.MustCompile /
// Compile / MustCompilePOSIX call whose first arg is a string constant)
// matching any of the cell-id family of banned patterns must live in
// kernel/metadata/contract_constraints.go (the single source) — anywhere
// else is a duplicate that drifts away from metadata.{Cell,Assembly}IDPattern
// and must be replaced with metadata.MatchCellID / metadata.MatchAssemblyID.
//
// Enforcement is split into two sub-tests:
//
//   - TestCellIDPatternSingleSource — typed-info scan: resolves regexp compile
//     callsites through *types.Info, catches aliased import and const-ref args.
//   - TestCellIDPatternSingleSourceLiterals — AST-only scan: walks every
//     BasicLit STRING node in every production .go file (tests=true), catches
//     bare string literals in godoc, error messages, test fixtures, and any
//     other non-call context that the typed scan misses.
//
// Together the two sub-tests close the roaming paths:
//
//	(a) regexp.MustCompile(literal) — caught by TestCellIDPatternSingleSource;
//	(b) bare literal in godoc / error msg / test fixture — caught by
//	    TestCellIDPatternSingleSourceLiterals.
//
// Remaining bypass paths (Medium, not Hard):
//   - split pattern across multiple consts / concat at call site — rule does
//     not chase indirection by design; review catches the odd shape.
//   - use a different-but-visually-similar regex (different anchor or char
//     class) — at that point it is a different pattern with different semantics.
//
// Banned literal strings (exact match, after string unquoting):
//   - "^[a-z][a-z0-9]+$" — current Cell/AssemblyIDPattern (≥2 chars, no dash);
//     defined in kernel/metadata/contract_constraints.go.
//   - "^[a-z][a-z0-9-]*$" — pre-PR-309 legacy form (allowed dash, allowed
//     single char); eliminated from runtime/auth and tools/archtest in this
//     PR and listed here to fail-closed against any re-introduction.
//
// Patterns with similar shape but different semantics (e.g.
// `^[a-z][a-z0-9-]+$` for kebab-case panic-reason ids in
// panic_invariants_test.go, or `^[a-z][a-z0-9]*(?:_[a-z0-9]+)*_ready$`
// for adapter ready-probe names) are NOT in the banned set — they are
// distinct, well-anchored sub-grammars unrelated to cell-id matching.
//
// AI-rebust: Medium. Detection uses *types.Info to resolve the callee to
// regexp.MustCompile (form-independent of `import r "regexp"` aliasing or
// `re := regexp.MustCompile` indirection) and exact string-content match
// on the first arg literal. Hard is not reachable in Go for this rule
// shape: regexp.MustCompile takes a plain string, and Go has no string-
// literal typing or sealed-by-pattern dispatch. Bypass paths: (a) write
// the pattern as a non-literal expression (string concat, var ref) — the
// rule does not chase indirection by design, and review will catch the
// odd shape; (b) tweak the regex semantics (different anchor, extra
// character class) — at that point it is a different pattern with
// different semantics, not a "looks-like cell id" duplicate.
//
// ref: kernel/metadata/contract_constraints.go — single source for cell-id family patterns + matchers (MatchCellID, MatchAssemblyID).
// ref: tools/archtest/prom_cell_label_funnel_test.go — companion typeseval callee-resolution range pattern.
// ref: .claude/rules/gocell/ai-collab.md — Medium archtest (typed-info + literal allowlist + path allowlist).
package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

const (
	cellIDPatternRuleID         = "CELL-ID-PATTERN-SINGLE-SOURCE-01"
	regexpPkgPath               = "regexp"
	cellIDPatternAllowFile      = "kernel/metadata/contract_constraints.go"
	cellIDPatternAllowSelfFile  = "tools/archtest/cell_id_pattern_single_source_test.go"
	legacyCallerCellRegexString = `^[a-z][a-z0-9-]*$`
)

// regexpCompileFuncs are the regexp package entry points that take a
// pattern string and produce a *regexp.Regexp. All three are gated by the
// same rule.
var regexpCompileFuncs = map[string]struct{}{
	"MustCompile":      {},
	"Compile":          {},
	"MustCompilePOSIX": {},
	"CompilePOSIX":     {},
}

// bannedPatterns is the set of literal regex strings that may only be
// compiled inside kernel/metadata/. Strings are computed once via the
// metadata constants so this archtest does not itself encode the same
// literal twice (single-source consistency with metadata.go).
//
// CellIDPattern and AssemblyIDPattern currently share the same string
// (^[a-z][a-z0-9]+$); building the set from a slice tolerates that
// equality without a map-literal duplicate-key compile error.
func bannedPatterns() map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range []string{
		metadata.CellIDPattern,
		metadata.AssemblyIDPattern,
		legacyCallerCellRegexString,
	} {
		out[p] = struct{}{}
	}
	return out
}

// TestCellIDPatternSingleSource enforces CELL-ID-PATTERN-SINGLE-SOURCE-01.
//
// Loads the production package set via the typed funnel
// typeseval.LoadProductionPackages (PRODUCTION-LOADER-FUNNEL-01 — the
// raw SharedResolver(_, _, _, "./...") form is banned in archtest test
// files) and walks every regexp.MustCompile family call. Records a
// violation when the first arg is a banned string literal and the file
// is not in the allowlist. Test files are also scanned (tests=true) so
// stray cell-id regex declared in *_test.go is caught.
func TestCellIDPatternSingleSource(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	module := readModulePath(t, root)

	resolver, err := typeseval.LoadProductionPackages(
		root, module, true /* tests */, typeseval.FlatNonDefaultTags())
	require.NoError(t, err, "LoadProductionPackages failed")

	allowFiles := map[string]struct{}{
		cellIDPatternAllowFile:     {},
		cellIDPatternAllowSelfFile: {},
	}
	banned := bannedPatterns()

	var violations []string
	for _, p := range resolver.Production() {
		if p == nil || p.TypesInfo == nil {
			continue
		}
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			rel, ok := fileroles.Rel(root, abs)
			if !ok {
				continue
			}
			if _, ok := allowFiles[rel]; ok {
				continue
			}
			violations = append(violations,
				scanCellIDPatternCalls(p, file, rel, banned)...)
		}
	}

	sort.Strings(violations)
	violations = dedupSortedStrings(violations)
	if len(violations) > 0 {
		t.Fatalf("%s: regexp.MustCompile/Compile of cell-id family pattern "+
			"must live in %s; use metadata.MatchCellID / "+
			"metadata.MatchAssemblyID elsewhere.\nViolations (%d):\n  %s",
			cellIDPatternRuleID, cellIDPatternAllowFile,
			len(violations), strings.Join(violations, "\n  "))
	}
}

// scanCellIDPatternCalls walks file's AST for regexp.MustCompile-family
// calls and returns one violation per banned-literal first arg.
func scanCellIDPatternCalls(
	p *packages.Package, file *ast.File, rel string, banned map[string]struct{},
) []string {
	var out []string
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isRegexpCompileCall(p.TypesInfo, call.Fun) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		lit := constLiteralValue(p.TypesInfo, call.Args[0])
		if lit == "" {
			return
		}
		if _, isBanned := banned[lit]; !isBanned {
			return
		}
		pos := p.Fset.Position(call.Pos())
		out = append(out, fmt.Sprintf(
			"%s:%d: regexp.MustCompile(%q) — banned cell-id family literal; "+
				"compile only in %s, callers should use metadata.MatchCellID",
			rel, pos.Line, lit, cellIDPatternAllowFile,
		))
	})
	return out
}

// isRegexpCompileCall resolves fun via *types.Info.Uses and reports
// whether it refers to one of regexp's MustCompile / Compile /
// MustCompilePOSIX / CompilePOSIX. Resolution through the type checker
// makes alias imports (`import r "regexp"`) and function-variable
// indirection (`mc := regexp.MustCompile`) match the same way.
func isRegexpCompileCall(info *types.Info, fun ast.Expr) bool {
	if info == nil {
		return false
	}
	var ident *ast.Ident
	switch v := fun.(type) {
	case *ast.Ident:
		ident = v
	case *ast.SelectorExpr:
		ident = v.Sel
	default:
		return false
	}
	obj := info.Uses[ident]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	if fn.Pkg().Path() != regexpPkgPath {
		return false
	}
	_, ok = regexpCompileFuncs[fn.Name()]
	return ok
}

// constLiteralValue extracts the string content of arg if it is a string
// literal or a constant string expression (e.g. metadata.CellIDPattern
// reference). Returns "" if arg is not a const-resolvable string.
//
// Const ref resolution uses *types.Info.Types, which carries the const
// value computed by the type checker; this catches the case where a
// caller spells the pattern as `metadata.CellIDPattern` rather than the
// raw `^[a-z][a-z0-9]+$` literal — both compile to the same banned
// outcome.
func constLiteralValue(info *types.Info, arg ast.Expr) string {
	if info == nil {
		return ""
	}
	tv, ok := info.Types[arg]
	if !ok || tv.Value == nil {
		return ""
	}
	if tv.Value.Kind() != constant.String {
		return ""
	}
	return constant.StringVal(tv.Value)
}

// dedupSortedStrings deduplicates an already-sorted slice in place. Used
// to fold duplicate diagnostics from packages.Visit's TestVariant overlap
// (the same file appears in both the `package x` and `package x_test`
// load when tests=true).
func dedupSortedStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s == out[len(out)-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// TestCellIDPatternSingleSourceLiterals enforces the BasicLit STRING half of
// CELL-ID-PATTERN-SINGLE-SOURCE-01.
//
// Walks every .go file in the module (tests=true) via packages.Visit and
// inspects every *ast.BasicLit of token.STRING. If the unquoted value equals
// any entry in bannedPatterns(), and the file is not in the allowlist, the
// test records a violation.
//
// This catches roaming paths that the typed-info regexp-callsite scan
// (TestCellIDPatternSingleSource) cannot reach: bare literals in godoc
// comments embedded in string fields, error message strings, test fixture
// expected-error strings, and comment-free bare assignments. It does NOT
// scan Go comments (only string literal nodes in the AST).
//
// Allowlist:
//   - kernel/metadata/contract_constraints.go — single source, may define the
//     pattern consts.
//   - tools/archtest/cell_id_pattern_single_source_test.go — self (the
//     bannedPatterns const + negative fixture use the strings).
func TestCellIDPatternSingleSourceLiterals(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	module := readModulePath(t, root)

	resolver, err := typeseval.LoadProductionPackages(
		root, module, true /* tests */, typeseval.FlatNonDefaultTags())
	require.NoError(t, err, "LoadProductionPackages failed")

	allowFiles := map[string]struct{}{
		cellIDPatternAllowFile:     {},
		cellIDPatternAllowSelfFile: {},
	}
	banned := bannedPatterns()

	var violations []string
	for _, p := range resolver.Production() {
		if p == nil {
			continue
		}
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			rel, ok := fileroles.Rel(root, abs)
			if !ok {
				continue
			}
			if _, ok := allowFiles[rel]; ok {
				continue
			}
			violations = append(violations,
				scanCellIDPatternLiterals(p.Fset, file, rel, banned)...)
		}
	}

	sort.Strings(violations)
	violations = dedupSortedStrings(violations)
	if len(violations) > 0 {
		t.Fatalf("%s: banned cell-id family pattern literal found outside "+
			"allowed files (%s, %s).\n"+
			"Use metadata.MatchCellID / metadata.CellIDPattern instead of "+
			"bare regex literals.\nViolations (%d):\n  %s",
			cellIDPatternRuleID, cellIDPatternAllowFile, cellIDPatternAllowSelfFile,
			len(violations), strings.Join(violations, "\n  "))
	}
}

// scanCellIDPatternLiterals walks file's AST for BasicLit STRING nodes whose
// unquoted value is a banned cell-id pattern. Returns one violation string per
// match.
func scanCellIDPatternLiterals(
	fset *token.FileSet, file *ast.File, rel string, banned map[string]struct{},
) []string {
	var out []string
	scanner.EachInSubtree[ast.BasicLit](file, func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING {
			return
		}
		unquoted, err := strconv.Unquote(lit.Value)
		if err != nil {
			return
		}
		if _, isBanned := banned[unquoted]; !isBanned {
			return
		}
		pos := fset.Position(lit.Pos())
		out = append(out, fmt.Sprintf(
			"%s:%d: string literal %q — banned cell-id family pattern; "+
				"use metadata.MatchCellID / metadata.CellIDPattern instead",
			rel, pos.Line, unquoted,
		))
	})
	return out
}

// TestCellIDPatternSingleSourceLiterals_NegativeFixture is a RED-path
// self-test that proves the BasicLit scanner catches the legacy banned
// pattern when present in a source string. It parses a synthetic Go snippet
// containing the legacy regex literal and asserts that scanCellIDPatternLiterals
// reports a violation.
//
// This fixture exists to satisfy the archtest charter requirement: new /
// modified archtest sub-tests must include a RED fixture proving the rule
// actually fires on a violating input.
func TestCellIDPatternSingleSourceLiterals_NegativeFixture(t *testing.T) {
	t.Parallel()

	// Synthetic source: a .go file that contains the legacy banned literal as a
	// string constant. The parser builds a real *ast.File from this text so
	// scanCellIDPatternLiterals exercises its full code path.
	const fakeSource = `package fake

const legacyPattern = "^[a-z][a-z0-9-]*$"
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake/fake.go", fakeSource, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}

	banned := bannedPatterns()
	violations := scanCellIDPatternLiterals(fset, file, "fake/fake.go", banned)

	if len(violations) == 0 {
		t.Fatal("RED fixture: expected scanCellIDPatternLiterals to report a " +
			"violation for the legacy '^[a-z][a-z0-9-]*$' literal, got none; " +
			"the BasicLit scan is broken")
	}
	// Positive assertion: the violation message must reference the banned string.
	if !strings.Contains(violations[0], legacyCallerCellRegexString) {
		t.Fatalf("RED fixture: violation message does not contain the banned "+
			"pattern %q; got: %s", legacyCallerCellRegexString, violations[0])
	}
}
