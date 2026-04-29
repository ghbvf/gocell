// prod_duration_const_internal_test.go — regression-guard unit tests for the
// PROD-DURATION-CONST-01 helper predicates. This file is the static safety net
// mandated by the "引入新约束必须同 PR 闭环" memory: every helper that the
// archtest enforcement relies on must have direct unit tests so a future refactor
// cannot silently widen or narrow the predicate without a red build.
package archtest

import (
	"go/ast"
	"go/parser"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseExpr is a test helper that parses a single Go expression and returns
// the ast.Expr. Failures are fatal — a bad source string is a test bug.
func parseExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	expr, err := parser.ParseExpr(src)
	require.NoError(t, err, "parseExpr(%q)", src)
	return expr
}

// callExpr extracts the *ast.CallExpr from a parsed expression. Panics on type
// mismatch (test bug, not production code path).
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
		// All 6 standard time units must return true.
		{"time.Nanosecond", true},
		{"time.Microsecond", true},
		{"time.Millisecond", true},
		{"time.Second", true},
		{"time.Minute", true},
		{"time.Hour", true},
		// time.Duration is a type, not a unit constant.
		{"time.Duration", false},
		// A non-time package — even if the field name matches.
		{"mytime.Second", false},
		// Bare identifier without selector.
		{"Second", false},
		// time.Sleep is a function call target — not a unit SelectorExpr.
		{"time.Now", false},
	}
	for _, c := range cases {
		c := c
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
		// The exact cast form.
		{"time.Duration(30)", true},
		{"time.Duration(x)", true},
		// A time unit used as a function — not the cast.
		{"time.Hour(30)", false},
		// Wrong package name.
		{"pkg.Duration(30)", false},
		// Correct package, wrong function.
		{"time.Sleep(30)", false},
	}
	for _, c := range cases {
		c := c
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
		// Non-zero integer literal — the happy path.
		{"5", true},
		{"30", true},
		{"100", true},
		// "0" is the idiomatic zero sentinel — must be rejected.
		{"0", false},
		// Product of two non-zero literals — chained magnitudes like 7*24.
		{"7*24", true},
		// Product containing an identifier — the magnitude includes a var.
		{"n*24", false},
		// time.Duration(<BasicLit>) counts as literal-bearing magnitude.
		{"time.Duration(30)", true},
		// time.Duration(<ident>) is not a literal.
		{"time.Duration(x)", false},
		// Parenthesised literal product.
		{"(7*24)", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.src, func(t *testing.T) {
			t.Parallel()
			got := allLiteralOrLitProduct(parseExpr(t, c.src))
			assert.Equal(t, c.want, got, "allLiteralOrLitProduct(%q)", c.src)
		})
	}
}

// ---------------------------------------------------------------------------
// TestIsLiteralDurationExpr
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
		// 30*24*time.Hour
		{"30*24*time.Hour", true},
		// time.Duration(<BasicLit>) cast form.
		{"time.Duration(30)", true},
		// time.Duration(<BasicLit>) * time.Unit.
		{"time.Duration(30)*time.Second", true},
		// Parenthesised form.
		{"(5*time.Second)", true},
		// Zero sentinel expressed as literal — zero magnitude rejected.
		{"0*time.Second", false},
		// Pure zero literal (not even a binary expr).
		{"0", false},
		// Named const scaling — existingDur*2 has no time.Unit on either side.
		{"existingDur*2", false},
		// time.Unit * named const — the other side is not allLiteralOrLitProduct.
		{"BaseRetryDelay*time.Second", false},
		// Non-time package unit.
		{"5*custompkg.Second", false},
		// A division expression (op != MUL) must not match.
		{"5*time.Hour/2", false},
		// time.Duration cast with runtime expr.
		{"time.Duration(x)", false},
		// Named const identifier alone.
		{"defaultTimeout", false},
		// SelectorExpr (struct field or package const).
		{"cfg.Timeout", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.src, func(t *testing.T) {
			t.Parallel()
			got := isLiteralDurationExpr(parseExpr(t, c.src))
			assert.Equal(t, c.want, got, "isLiteralDurationExpr(%q)", c.src)
		})
	}
}

// ---------------------------------------------------------------------------
// TestUnwrapNowAddCall
// ---------------------------------------------------------------------------

func TestUnwrapNowAddCall(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src       string
		wantOK    bool
		wantInner string
	}{
		// Standard form used in context.WithDeadline.
		{"time.Now().Add(5*time.Second)", true, "5*time.Second"},
		// Named const in the Add argument — still unwraps (inner not literal).
		{"time.Now().Add(defaultTimeout)", true, "defaultTimeout"},
		// Wrong method — Sub, not Add.
		{"time.Now().Sub(5*time.Second)", false, ""},
		// Receiver is not a call (bare ident).
		{"now.Add(5*time.Second)", false, ""},
		// Correct selector but receiver is not time.Now() — some other Now.
		{"clock.Now().Add(5*time.Second)", false, ""},
		// No arguments to Add.
		{"time.Now().Add()", false, ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.src, func(t *testing.T) {
			t.Parallel()
			inner, ok := unwrapNowAddCall(parseExpr(t, c.src))
			assert.Equal(t, c.wantOK, ok, "unwrapNowAddCall(%q) ok", c.src)
			if c.wantOK && ok {
				assert.Equal(t, c.wantInner, prodExprText(inner),
					"unwrapNowAddCall(%q) inner", c.src)
			}
		})
	}
}
