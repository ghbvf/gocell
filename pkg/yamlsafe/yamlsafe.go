// Package yamlsafe provides a typed YAML scalar that safely round-trips through
// plain-style YAML emission. Using the Scalar type prevents callers from passing
// raw user input directly into scaffold templates where YAML-meta characters
// (colon, braces, quotes, newlines, etc.) would break YAML structure or allow
// injection of adjacent fields.
//
// # Usage
//
//	s := yamlsafe.Quote(userInput) // always safe to embed in a YAML value
//	fmt.Fprintf(w, "id: %s\n", s)  // Scalar.String() returns the quoted form
//
// # AI-Hard contract
//
// Quote is the single funnel for YAML scalar quoting in scaffold templates.
// Direct string interpolation into YAML templates is statically detected by
// archtest SCAFFOLD-WRITE-FUNNEL-01 (path safety) and YAML-QUOTE-FUNNEL-01
// (every yamlsafe.Scalar(...) type conversion outside this package must
// have a yamlsafe.Quote(...) call as its argument).
package yamlsafe

import (
	"fmt"
	"strings"
)

// Scalar is a typed YAML scalar that safely round-trips through plain-style
// YAML emission. Use Quote to construct from raw user input; the type system
// then prevents callers from passing raw strings to scaffold templates.
type Scalar string

// String returns the rendered scalar for template emission.
func (s Scalar) String() string { return string(s) }

// containsControlNeedsDoubleQuote reports whether raw contains any C0/DEL
// control character (except TAB \x09) that requires double-quoted YAML
// emission: \n \r \x00 and all other C0 bytes (\x01-\x08, \x0b, \x0c,
// \x0e-\x1f) plus DEL (\x7f). TAB is excluded because YAML 1.2 §5.1
// permits it in scalars without quoting.
func containsControlNeedsDoubleQuote(raw string) bool {
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b < 0x20 && b != '\t' {
			return true
		}
		if b == 0x7f {
			return true
		}
	}
	return false
}

// Quote constructs a Scalar from raw user input. If raw contains characters
// that cannot safely render as plain-style YAML (newline / NUL / other C0 /
// DEL / leading whitespace / : { } [ ] , & * # ? | > ! % @ ` " '), the result
// is single-quoted with embedded single quotes doubled. Strings containing
// C0/DEL control characters (except TAB) use double-quoted form with escape
// sequences so they round-trip correctly through yaml.Unmarshal.
//
// This is the single funnel for YAML scalar quoting in scaffold templates.
func Quote(raw string) Scalar {
	if !needsQuoting(raw) {
		return Scalar(raw)
	}
	// Strings with C0/DEL control characters (except TAB) cannot be faithfully
	// represented in single-quoted YAML scalars. Use double-quoted form with
	// escape sequences instead so they round-trip correctly.
	if containsControlNeedsDoubleQuote(raw) {
		return Scalar(doubleQuote(raw))
	}
	// single-quoted YAML scalar; embedded single quotes doubled
	escaped := strings.ReplaceAll(raw, "'", "''")
	return Scalar("'" + escaped + "'")
}

// doubleQuote returns a YAML double-quoted scalar for raw, escaping
// backslashes, double-quotes, newlines, carriage-returns, NUL bytes, and all
// other C0/DEL control characters (\x01-\x08, \x0b, \x0c, \x0e-\x1f, \x7f)
// via \xHH hex escapes so they round-trip through yaml.Unmarshal.
func doubleQuote(raw string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range raw {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\x00':
			b.WriteString(`\0`)
		default:
			// C0 control characters (except TAB which is safe) and DEL need hex escapes.
			if (r < 0x20 && r != '\t') || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// yamlNullTokens are the plain scalars yaml.v3 resolves to null per the
// YAML 1.2 core schema (§10.3.2). A raw string equal to any of these must
// be quoted: unquoted, it round-trips to "" (the zero value) instead of the
// literal text — a silent data-loss / type-confusion injection in scaffold
// values. Bool/int/float plain scalars do NOT need this guard because
// yaml.v3 coerces them back to their source text when the decode target is
// a string; only null collapses to the zero value.
var yamlNullTokens = map[string]struct{}{
	"~": {}, "null": {}, "Null": {}, "NULL": {},
}

// hasLeadingWhitespace reports whether raw begins with a space or TAB.
// yaml.v3 strips leading whitespace from plain scalars on round-trip, so
// such a value must be quoted to survive.
func hasLeadingWhitespace(raw string) bool {
	return strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")
}

// hasTrailingWhitespace reports whether raw ends with a space or TAB.
// Trailing whitespace is stripped from plain scalars by yaml.v3, so a
// value ending in space or tab would silently lose characters on
// round-trip. raw must be non-empty (callers reject "" first).
func hasTrailingWhitespace(raw string) bool {
	last := raw[len(raw)-1]
	return last == ' ' || last == '\t'
}

// hasPlainStyleIndicatorPrefix reports whether raw starts with a YAML
// plain-style indicator (`-` / `?` / `:`) that is followed by space, tab,
// EOL, or is the entire scalar (YAML 1.2 §6.3.5). Quoting forces literal
// scalar interpretation and prevents the value from being parsed as a
// block sequence entry, explicit mapping key, or mapping value indicator.
// raw must be non-empty (callers reject "" first).
func hasPlainStyleIndicatorPrefix(raw string) bool {
	first := raw[0]
	if first != '-' && first != '?' && first != ':' {
		return false
	}
	return len(raw) == 1 || raw[1] == ' ' || raw[1] == '\t' || raw[1] == '\n' || raw[1] == '\r'
}

// needsQuoting reports whether raw must be wrapped in quotes for safe
// YAML plain-style emission. Returns true for empty strings, leading or
// trailing whitespace, leading block/mapping indicators, document marker
// strings, YAML null tokens, C0/DEL control characters (except TAB) per
// YAML 1.2 §5.1, and any YAML-meta character. Each predicate is factored
// into a single-purpose helper; the C0/DEL scan reuses
// containsControlNeedsDoubleQuote (single source of that classification).
func needsQuoting(raw string) bool {
	if raw == "" {
		return true
	}
	if hasLeadingWhitespace(raw) {
		return true
	}
	if hasPlainStyleIndicatorPrefix(raw) {
		return true
	}
	if hasTrailingWhitespace(raw) {
		return true
	}
	// Document marker lines must be quoted to prevent consumer parsers from
	// treating the scalar value as a document boundary or end-of-document marker.
	if raw == "---" || raw == "..." {
		return true
	}
	if _, isNull := yamlNullTokens[raw]; isNull {
		return true
	}
	// YAML 1.2 §5.1 — C0/DEL control characters (except TAB) cannot appear
	// in scalars without escaping; route through doubleQuote so they round-trip.
	if containsControlNeedsDoubleQuote(raw) {
		return true
	}
	// YAML-meta characters: colon, braces, brackets, comma, special indicators,
	// newlines and NUL. Single-quote included so embedded quotes are doubled.
	return strings.ContainsAny(raw, ":{}[],&*#?|>!%@`\"'\n\r\x00")
}
