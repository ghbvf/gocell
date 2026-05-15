package scanner

import (
	"go/ast"
	"go/token"
	"reflect"
	"strconv"
	"strings"
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

// StructTagJSONKey extracts the JSON key from an *ast.Field's tag literal and
// reports whether it equals the given key.
//
// In the Go AST a struct tag is stored as *ast.BasicLit with Kind=STRING and
// Value a raw-string literal `json:"authz_epoch"`. A bare literal scan for
// "authz_epoch" would NOT match because the full tag value is
// `json:"authz_epoch"` — the outer backticks are not stripped by a simple
// StartsWith/Contains check, and the inner quotes are escaped. This helper
// normalises the tag by:
//
//  1. Stripping the outer backtick delimiters (raw string literal) — since
//     struct tags in Go are always raw string literals enclosed in backticks.
//  2. Parsing the result as [reflect.StructTag] (Go's canonical tag format).
//  3. Extracting the "json" key and stripping any comma-separated options
//     (e.g. `json:"authz_epoch,omitempty"` → `authz_epoch`).
//
// Returns false if tag is nil, Kind != STRING, or the tag value does not use
// backtick delimiters (malformed tag AST).
//
// Blind-spot: the helper does not handle dynamically-constructed struct tags
// (reflect.StructTag set at runtime), which would be invisible to any AST
// scanner. Dynamic tags are an unusual pattern; archtest consumer tests assert
// their absence.
//
// ref: reflect.StructTag.Get — Go stdlib struct tag parsing.
// ref: golang/go src/go/ast/ast.go Field.Tag — *ast.BasicLit with Kind=STRING.
func StructTagJSONKey(tag *ast.BasicLit, jsonKey string) bool {
	if tag == nil || tag.Kind != token.STRING {
		return false
	}
	raw := tag.Value
	// Struct tags are always raw string literals enclosed in backticks.
	if len(raw) < 2 || raw[0] != '`' || raw[len(raw)-1] != '`' {
		return false
	}
	inner := raw[1 : len(raw)-1]
	got := reflect.StructTag(inner).Get("json")
	// Strip comma-separated options (e.g. "authz_epoch,omitempty").
	if idx := strings.IndexByte(got, ','); idx >= 0 {
		got = got[:idx]
	}
	return got == jsonKey
}
