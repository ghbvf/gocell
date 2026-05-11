// Package archtest — production code invariant gates.
//
// Asserted invariants:
//   - INVARIANT: PROD-DURATION-CONST-01
//
// Merged from:
//   - prod_duration_const_test.go          (PROD-DURATION-CONST-01 enforcement + helpers)
//   - prod_duration_const_internal_test.go (PROD-DURATION-CONST-01 predicate unit tests)
//
// NOTE: prod_duration_fixtures_test.go is a fixture-driver file and is NOT merged here.
//
// ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ============================================================
// INVARIANT: PROD-DURATION-CONST-01
// ============================================================
//
// In all production-shippable .go files, any expression whose static type is
// time.Duration and whose subtree contains a BasicLit must appear directly in
// the initializer of a package-level const declaration. All other positions
// are violations. Exception: a BasicLit whose token value is "0" is not a
// violation.
//
// ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6

// TestProdDurationConst enforces PROD-DURATION-CONST-01 using universal AST
// walk: for every declaration that is not a package-level const block, any
// expression whose static type is time.Duration and whose subtree contains a
// BasicLit is a violation.
func TestProdDurationConst(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production packages module-wide, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	pkgs, errs, err := typeseval.LoadPackages(root, false, []string{"e2e", "integration", "pg"}, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			rel, ok := fileroles.Rel(root, abs)
			if !ok || !fileroles.IsProductionCode(rel) {
				continue
			}

			violations = append(violations, scanProdDurationAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"PROD-DURATION-CONST-01: extract literal durations to package-level const. "+
			"ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6")
}

// scanProdDurationAST walks a single parsed file's AST using a universal walk:
// for each top-level declaration that is not a package-level const block, it
// inspects every sub-expression. An expression that (a) has static type
// time.Duration and (b) whose subtree contains a BasicLit is a violation.
//
// Implementation: scanner.EachNode is preorder-only (no proceed-bool), so
// we collect candidate hits across every concrete Expr kind that
// isLiteralDurationExpr can recognize standalone (BinaryExpr/CallExpr/
// UnaryExpr/ParenExpr/BasicLit), sort by start position, and drop any hit
// fully contained inside an outer hit (outer-wins range dedup). This
// preserves the "report only the outermost match" semantics — without the
// dedup, `time.Duration(30)*time.Second` would be reported twice (the outer
// BinaryExpr and the inner CallExpr both match).
func scanProdDurationAST(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []string {
	type hit struct {
		expr ast.Expr
		pos  token.Pos
		end  token.Pos
	}
	var violations []string

	checkExpr := func(root ast.Node) {
		var hits []hit
		consider := func(expr ast.Expr) {
			if !exprIsTimeDuration(expr, info) {
				return
			}
			if !isLiteralDurationExpr(expr) {
				return
			}
			hits = append(hits, hit{expr: expr, pos: expr.Pos(), end: expr.End()})
		}
		// Cover every concrete Expr node kind that isLiteralDurationExpr can
		// recognize: BasicLit (var x time.Duration = 5), BinaryExpr
		// (5*time.Second), CallExpr (time.Duration(5)), UnaryExpr
		// (-5*time.Second), ParenExpr ((5*time.Second)).
		scanner.EachInSubtree[ast.BinaryExpr](root, func(e *ast.BinaryExpr) { consider(e) })
		scanner.EachInSubtree[ast.CallExpr](root, func(e *ast.CallExpr) { consider(e) })
		scanner.EachInSubtree[ast.UnaryExpr](root, func(e *ast.UnaryExpr) { consider(e) })
		scanner.EachInSubtree[ast.ParenExpr](root, func(e *ast.ParenExpr) { consider(e) })
		scanner.EachInSubtree[ast.BasicLit](root, func(e *ast.BasicLit) { consider(e) })

		// Outer-wins dedup: sort by start ascending, then drop any hit fully
		// contained inside the most recent retained hit's [pos,end] range.
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].pos != hits[j].pos {
				return hits[i].pos < hits[j].pos
			}
			return hits[i].end > hits[j].end // wider range first when starts tie
		})
		var lastEnd token.Pos
		for _, h := range hits {
			if h.pos < lastEnd {
				continue // contained inside a previously retained outer hit
			}
			pos := fset.Position(h.pos)
			violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, pos.Line, formatDurationExpr(h.expr)))
			lastEnd = h.end
		}
	}

	// Paired-index iteration: only top-level Decls are scanned; nested decls
	// inside a func body / spec value belong to other passes.
	for i := range file.Decls {
		decl := file.Decls[i]
		if gd, ok := decl.(*ast.GenDecl); ok {
			if gd.Tok == token.CONST {
				// Package-level const blocks are the unique compliant position — skip.
				continue
			}
			checkExpr(gd)
			continue
		}
		checkExpr(decl)
	}

	return violations
}

// exprIsTimeDuration returns true when expr's static type is time.Duration.
func exprIsTimeDuration(expr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == "time" && obj.Name() == "Duration"
}

// isLiteralDurationExpr returns true for expressions whose subtree contains a
// numeric BasicLit that contributes a non-zero literal value.
func isLiteralDurationExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return (e.Kind == token.INT || e.Kind == token.FLOAT) && e.Value != "0"
	case *ast.BinaryExpr:
		if e.Op != token.MUL {
			return false
		}
		if isTimeUnit(e.X) && allLiteralOrLitProduct(e.Y) {
			return true
		}
		if isTimeUnit(e.Y) && allLiteralOrLitProduct(e.X) {
			return true
		}
		return false
	case *ast.ParenExpr:
		return isLiteralDurationExpr(e.X)
	case *ast.UnaryExpr:
		return isLiteralDurationExpr(e.X)
	case *ast.CallExpr:
		if isTimeDurationCast(e) && len(e.Args) == 1 {
			if lit, ok := e.Args[0].(*ast.BasicLit); ok {
				return (lit.Kind == token.INT || lit.Kind == token.FLOAT) && lit.Value != "0"
			}
		}
	}
	return false
}

// isTimeUnit returns true for time.{Nanosecond,Microsecond,Millisecond,Second,Minute,Hour}.
func isTimeUnit(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "time" {
		return false
	}
	switch sel.Sel.Name {
	case "Nanosecond", "Microsecond", "Millisecond",
		"Second", "Minute", "Hour":
		return true
	}
	return false
}

// isTimeDurationCast returns true for time.Duration(<expr>) type-conversion calls.
func isTimeDurationCast(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "time" {
		return false
	}
	return sel.Sel.Name == "Duration"
}

// allLiteralOrLitProduct reports whether expr is composed entirely of BasicLit
// nodes, parenthesised/arithmetic BasicLit products, or time.Duration(<BasicLit>)
// casts.
func allLiteralOrLitProduct(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return (e.Kind == token.INT || e.Kind == token.FLOAT) && e.Value != "0"
	case *ast.ParenExpr:
		return allLiteralOrLitProduct(e.X)
	case *ast.BinaryExpr:
		return allLiteralOrLitProduct(e.X) && allLiteralOrLitProduct(e.Y)
	case *ast.UnaryExpr:
		return allLiteralOrLitProduct(e.X)
	case *ast.CallExpr:
		if isTimeDurationCast(e) && len(e.Args) == 1 {
			if lit, ok := e.Args[0].(*ast.BasicLit); ok {
				return (lit.Kind == token.INT || lit.Kind == token.FLOAT) && lit.Value != "0"
			}
		}
	}
	return false
}

// formatDurationExpr renders an expression back to compact human-readable text
// for violation reports.
func formatDurationExpr(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Value
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name + "." + e.Sel.Name
		}
		return e.Sel.Name
	case *ast.BinaryExpr:
		return formatDurationExpr(e.X) + e.Op.String() + formatDurationExpr(e.Y)
	case *ast.ParenExpr:
		return "(" + formatDurationExpr(e.X) + ")"
	case *ast.UnaryExpr:
		return e.Op.String() + formatDurationExpr(e.X)
	case *ast.CallExpr:
		args := make([]string, len(e.Args))
		for i, a := range e.Args {
			args[i] = formatDurationExpr(a)
		}
		return formatDurationExpr(e.Fun) + "(" + strings.Join(args, ", ") + ")"
	}
	return "<expr>"
}

// ============================================================
// PROD-DURATION-CONST-01 predicate unit tests
// ============================================================
//
// These tests guard the helper predicates to ensure a future refactor cannot
// silently widen or narrow the predicate without a red build.

// parseExpr parses a single Go expression. Failures are fatal — bad source is a test bug.
func parseExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	expr, err := parser.ParseExpr(src)
	require.NoError(t, err, "parseExpr(%q)", src)
	return expr
}

// callExpr extracts *ast.CallExpr from a parsed expression. Panics on type mismatch.
func callExpr(t *testing.T, src string) *ast.CallExpr {
	t.Helper()
	e := parseExpr(t, src)
	call, ok := e.(*ast.CallExpr)
	require.True(t, ok, "expected *ast.CallExpr from %q, got %T", src, e)
	return call
}

func TestIsTimeUnit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src  string
		want bool
	}{
		{"time.Nanosecond", true},
		{"time.Microsecond", true},
		{"time.Millisecond", true},
		{"time.Second", true},
		{"time.Minute", true},
		{"time.Hour", true},
		{"time.Duration", false},
		{"mytime.Second", false},
		{"Second", false},
		{"time.Now", false},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			t.Parallel()
			got := isTimeUnit(parseExpr(t, c.src))
			assert.Equal(t, c.want, got, "isTimeUnit(%q)", c.src)
		})
	}
}

func TestIsTimeDurationCast(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src  string
		want bool
	}{
		{"time.Duration(30)", true},
		{"time.Duration(x)", true},
		{"time.Hour(30)", false},
		{"pkg.Duration(30)", false},
		{"time.Sleep(30)", false},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			t.Parallel()
			got := isTimeDurationCast(callExpr(t, c.src))
			assert.Equal(t, c.want, got, "isTimeDurationCast(%q)", c.src)
		})
	}
}

func TestAllLiteralOrLitProduct(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src  string
		want bool
	}{
		{"5", true},
		{"30", true},
		{"100", true},
		{"0", false},
		{"7*24", true},
		{"n*24", false},
		{"time.Duration(30)", true},
		{"time.Duration(x)", false},
		{"(7*24)", true},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			t.Parallel()
			got := allLiteralOrLitProduct(parseExpr(t, c.src))
			assert.Equal(t, c.want, got, "allLiteralOrLitProduct(%q)", c.src)
		})
	}
}

func TestIsLiteralDurationExpr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src  string
		want bool
	}{
		{"5*time.Second", true},
		{"30*time.Second", true},
		{"10*time.Minute", true},
		{"60*time.Second", true},
		{"time.Second*5", true},
		{"7*24*time.Hour", true},
		{"30*24*time.Hour", true},
		{"time.Duration(30)", true},
		{"time.Duration(30)*time.Second", true},
		{"(5*time.Second)", true},
		{"-5*time.Second", true},
		{"5", true},
		{"0", false},
		{"0*time.Second", false},
		{"existingDur*2", false},
		{"BaseRetryDelay*time.Second", false},
		{"5*custompkg.Second", false},
		{"5*time.Hour/2", false},
		{"time.Duration(x)", false},
		{"defaultTimeout", false},
		{"cfg.Timeout", false},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			t.Parallel()
			got := isLiteralDurationExpr(parseExpr(t, c.src))
			assert.Equal(t, c.want, got, "isLiteralDurationExpr(%q)", c.src)
		})
	}
}

// buildTimeDurationInfo returns a synthetic *types.Info that maps a dummy
// ast.Ident to type time.Duration.
func buildTimeDurationInfo(expr ast.Expr, isDuration bool) *types.Info {
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
	}
	if !isDuration {
		return info
	}
	timePkg := types.NewPackage("time", "time")
	durObj := types.NewTypeName(token.NoPos, timePkg, "Duration", nil)
	durType := types.NewNamed(durObj, types.Typ[types.Int64], nil)
	info.Types[expr] = types.TypeAndValue{Type: durType}
	return info
}

func TestExprIsTimeDuration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		desc       string
		src        string
		isDuration bool
		want       bool
	}{
		{
			desc:       "5*time.Second mapped to time.Duration → true",
			src:        "5*time.Second",
			isDuration: true,
			want:       true,
		},
		{
			desc:       "5 mapped to time.Duration → true",
			src:        "5",
			isDuration: true,
			want:       true,
		},
		{
			desc:       "5*int(0) — not time.Duration in info → false",
			src:        "5",
			isDuration: false,
			want:       false,
		},
		{
			desc:       "expr not present in info → false",
			src:        "x",
			isDuration: false,
			want:       false,
		},
		{
			desc:       "nil info → false",
			src:        "5*time.Second",
			isDuration: false,
			want:       false,
		},
		{
			desc:       "time.Duration(30) mapped to time.Duration → true",
			src:        "time.Duration(30)",
			isDuration: true,
			want:       true,
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			t.Parallel()
			expr := parseExpr(t, c.src)
			var info *types.Info
			if c.desc != "nil info → false" {
				info = buildTimeDurationInfo(expr, c.isDuration)
			}
			got := exprIsTimeDuration(expr, info)
			assert.Equal(t, c.want, got, "exprIsTimeDuration for %q", c.src)
		})
	}
}

// countDurationLiteralsInFile counts how many times isLiteralDurationExpr
// returns true when walking all non-package-const decls in the file.
func countDurationLiteralsInFile(t *testing.T, src string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "f.go", src, 0)
	require.NoError(t, err)

	// Outer-wins dedup mirrors scanProdDurationAST: collect candidate hits over
	// all expr shapes (BinaryExpr, CallExpr, UnaryExpr, ParenExpr, BasicLit),
	// drop any hit fully contained inside another. This count test does NOT
	// gate on exprIsTimeDuration (no types.Info here), so the BasicLit pass
	// would over-count integer literals everywhere — restrict to outer kinds
	// that isLiteralDurationExpr can recognize standalone.
	countInRoot := func(root ast.Node) int {
		type hit struct{ pos, end token.Pos }
		var hits []hit
		consider := func(expr ast.Expr) {
			if isLiteralDurationExpr(expr) {
				hits = append(hits, hit{pos: expr.Pos(), end: expr.End()})
			}
		}
		scanner.EachInSubtree[ast.BinaryExpr](root, func(e *ast.BinaryExpr) { consider(e) })
		scanner.EachInSubtree[ast.CallExpr](root, func(e *ast.CallExpr) { consider(e) })
		scanner.EachInSubtree[ast.UnaryExpr](root, func(e *ast.UnaryExpr) { consider(e) })
		scanner.EachInSubtree[ast.ParenExpr](root, func(e *ast.ParenExpr) { consider(e) })
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].pos != hits[j].pos {
				return hits[i].pos < hits[j].pos
			}
			return hits[i].end > hits[j].end
		})
		n := 0
		var lastEnd token.Pos
		for _, h := range hits {
			if h.pos < lastEnd {
				continue
			}
			n++
			lastEnd = h.end
		}
		return n
	}

	count := 0
	// Paired-index iteration over file.Decls: avoids path B's `for _, X :=`
	// + type-dispatch pattern. Semantics unchanged — only top-level decls
	// contribute to the count (nested decls inside func bodies do not).
	for i := range f.Decls {
		decl := f.Decls[i]
		if gd, ok := decl.(*ast.GenDecl); ok {
			if gd.Tok == token.CONST {
				// Package-level const blocks are the unique compliant position — skip.
				continue
			}
			count += countInRoot(gd)
			continue
		}
		count += countInRoot(decl)
	}
	return count
}

func TestUniformWalkSkipsPackageConst(t *testing.T) {
	t.Parallel()

	cases := []struct {
		desc  string
		src   string
		count int
	}{
		{
			desc: "package-level const x = 5*time.Second — zero violations",
			src: `package p
import "time"
const x = 5 * time.Second
`,
			count: 0,
		},
		{
			desc: "const block with two entries — zero violations",
			src: `package p
import "time"
const (
	a = 5 * time.Second
	b = 7 * 24 * time.Hour
)
`,
			count: 0,
		},
		{
			desc: "func-local const — one violation",
			src: `package p
import "time"
func f() {
	const x = 5 * time.Second
	_ = x
}
`,
			count: 1,
		},
		{
			desc: "var at package level — one violation",
			src: `package p
import "time"
var x = 5 * time.Second
`,
			count: 1,
		},
		{
			desc: "package-level const and var — only var is a violation",
			src: `package p
import "time"
const ok = 5 * time.Second
var bad = 30 * time.Second
`,
			count: 1,
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			t.Parallel()
			got := countDurationLiteralsInFile(t, c.src)
			assert.Equal(t, c.count, got, "violation count for: %s", c.desc)
		})
	}
}

func TestUniformWalkUnwraps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		desc  string
		src   string
		count int
	}{
		{
			desc: "time.Sleep(5*time.Second) — one violation at BinaryExpr",
			src: `package p
import "time"
func f() { time.Sleep(5 * time.Second) }
`,
			count: 1,
		},
		{
			desc: "nested call f(g(5*time.Second)) — one violation at innermost BinaryExpr",
			src: `package p
import "time"
func g(d time.Duration) time.Duration { return d }
func h(d time.Duration) {}
func f() { h(g(5 * time.Second)) }
`,
			count: 1,
		},
		{
			desc: "two independent literals — two violations",
			src: `package p
import "time"
func f() {
	time.Sleep(5 * time.Second)
	time.Sleep(30 * time.Second)
}
`,
			count: 2,
		},
		{
			desc: "addition of two literal durations — two violations",
			src: `package p
import "time"
var x = 5*time.Second + 30*time.Millisecond
`,
			count: 2,
		},
		{
			desc: "named const passed to Sleep — zero violations",
			src: `package p
import "time"
const d = 5 * time.Second
func f() { time.Sleep(d) }
`,
			count: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			t.Parallel()
			got := countDurationLiteralsInFile(t, c.src)
			assert.Equal(t, c.count, got, "violation count for: %s", c.desc)
		})
	}
}
