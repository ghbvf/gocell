// prod_duration_const_internal_test.go — regression-guard unit tests for the
// PROD-DURATION-CONST-01 helper predicates. Every helper that the archtest
// enforcement relies on has direct unit tests so a future refactor cannot
// silently widen or narrow the predicate without a red build.
//
// ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// ---------------------------------------------------------------------------
// TestIsTimeUnit
// ---------------------------------------------------------------------------

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
		// time.Duration is a type, not a unit constant.
		{"time.Duration", false},
		// Non-time package — field name matches but wrong receiver.
		{"mytime.Second", false},
		// Bare identifier without selector.
		{"Second", false},
		// time.Now is a function, not a unit.
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

// ---------------------------------------------------------------------------
// TestIsTimeDurationCast
// ---------------------------------------------------------------------------

func TestIsTimeDurationCast(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src  string
		want bool
	}{
		{"time.Duration(30)", true},
		{"time.Duration(x)", true},
		// time.Hour used as function — not the cast.
		{"time.Hour(30)", false},
		// Wrong package name.
		{"pkg.Duration(30)", false},
		// Correct package, wrong function.
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

// ---------------------------------------------------------------------------
// TestAllLiteralOrLitProduct
// ---------------------------------------------------------------------------

func TestAllLiteralOrLitProduct(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src  string
		want bool
	}{
		{"5", true},
		{"30", true},
		{"100", true},
		// Zero sentinel must be rejected.
		{"0", false},
		// Product of two non-zero literals.
		{"7*24", true},
		// Product containing an identifier.
		{"n*24", false},
		// time.Duration(<BasicLit>) counts as literal-bearing magnitude.
		{"time.Duration(30)", true},
		// time.Duration(<ident>) is not a literal.
		{"time.Duration(x)", false},
		// Parenthesised literal product.
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

// ---------------------------------------------------------------------------
// TestIsLiteralDurationExpr — includes new BasicLit single-node cases
// ---------------------------------------------------------------------------

func TestIsLiteralDurationExpr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src  string
		want bool
	}{
		// Standard literal * unit forms.
		{"5*time.Second", true},
		{"30*time.Second", true},
		{"10*time.Minute", true},
		{"60*time.Second", true},
		// time.Unit * literal (reversed order).
		{"time.Second*5", true},
		// Chained magnitude: 7*24*time.Hour.
		{"7*24*time.Hour", true},
		{"30*24*time.Hour", true},
		// time.Duration(<BasicLit>) cast form.
		{"time.Duration(30)", true},
		// time.Duration(<BasicLit>) * time.Unit.
		{"time.Duration(30)*time.Second", true},
		// Parenthesised form.
		{"(5*time.Second)", true},
		// Negated form — -5*time.Second must still flag.
		{"-5*time.Second", true},

		// --- BasicLit single-node cases (彻底化关键新增) ---
		// Non-zero bare literal — var x time.Duration = 5 scenario.
		// isLiteralDurationExpr sees a *ast.BasicLit("5") → true.
		{"5", true},
		// Zero bare literal — return 0 sentinel → must NOT flag.
		{"0", false},

		// Zero sentinel expressed as multiplication — magnitude "0" rejected.
		{"0*time.Second", false},
		// Named const scaling: existingDur*2.
		// No recursive fallback — namedVar*scalar must not flag even without type guard.
		{"existingDur*2", false},
		// time.Unit * named const — allLiteralOrLitProduct(BaseRetryDelay) = false.
		{"BaseRetryDelay*time.Second", false},
		// Non-time package unit: 5*custompkg.Second.
		// isTimeUnit(custompkg.Second)=false, allLiteralOrLitProduct(5)=true but
		// the other side is not a time.Unit, so terminal check fails.
		// No recursive fallback → false.
		{"5*custompkg.Second", false},
		// Division expression (op != MUL).
		{"5*time.Hour/2", false},
		// time.Duration cast with runtime expr.
		{"time.Duration(x)", false},
		// Named const identifier alone.
		{"defaultTimeout", false},
		// SelectorExpr (struct field or package const).
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

// ---------------------------------------------------------------------------
// TestExprIsTimeDuration — type-guard unit tests using synthetic types.Info
// ---------------------------------------------------------------------------

// buildTimeDurationInfo returns a synthetic *types.Info that maps a dummy
// ast.Ident to type time.Duration via a hand-crafted *types.Named object.
// This lets us test exprIsTimeDuration without a full packages.Load.
func buildTimeDurationInfo(expr ast.Expr, isDuration bool) *types.Info {
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
	}
	if !isDuration {
		return info
	}
	// Construct a *types.Named whose Obj reports pkg="time", name="Duration".
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
			isDuration: false, // will test with nil info directly
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

// ---------------------------------------------------------------------------
// TestUniformWalkSkipsPackageConst — universal walk skips package-level const
// ---------------------------------------------------------------------------

// countDurationLiteralsInFile counts how many times isLiteralDurationExpr
// returns true when walking all non-package-const decls in the file.
// The type guard (exprIsTimeDuration) is bypassed intentionally — these tests
// focus on the walk/skip logic and predicate shape, not on type resolution.
// In production, exprIsTimeDuration would further filter to only time.Duration
// expressions; here every literal-shaped expr is counted.
func countDurationLiteralsInFile(t *testing.T, src string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "f.go", src, 0)
	require.NoError(t, err)

	count := 0
	for _, decl := range f.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			continue // package-level const — skip entire subtree
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			expr, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			if !isLiteralDurationExpr(expr) {
				return true
			}
			count++
			return false
		})
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

// ---------------------------------------------------------------------------
// TestUniformWalkUnwraps — walk reports outermost match only, not sub-exprs
// ---------------------------------------------------------------------------

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
