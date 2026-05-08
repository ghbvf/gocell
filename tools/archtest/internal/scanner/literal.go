package scanner

import (
	"go/ast"
	"go/token"
	"strconv"
)

// StringLitValue returns the unquoted value of a string literal AST node.
// Both interpreted strings (`"foo"`) and raw strings (“ `foo` “) are
// normalized through [strconv.Unquote], so escape sequences such as `\x61`,
// `a`, and `\n` are decoded. The returned ok is false in any of the
// following cases:
//
//   - lit is nil
//   - lit.Kind is not [token.STRING] (e.g. rune literals like `'a'`,
//     numeric literals, etc.)
//   - lit.Value is malformed and [strconv.Unquote] returns an error
//
// Callers should treat ok=false as "not a usable string literal" and either
// surface a diagnostic or skip the node — never fall back to the raw
// lit.Value because that would defeat the normalization contract.
//
// Note: this helper does not handle BinaryExpr-based concatenation
// (`"ad" + "min"`). Such forms intentionally fall through to ok=false; rules
// that need to detect them must walk BinaryExpr explicitly.
//
// ref: golang/go src/strconv/quote.go — Unquote handles both `"..."` and
// “ `...` “ forms; rune literals require a different code path which we
// reject here so STRING-only contracts stay strict.
func StringLitValue(lit *ast.BasicLit) (string, bool) {
	if lit == nil || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}
