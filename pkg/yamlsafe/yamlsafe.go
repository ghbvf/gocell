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

// needsQuoting reports whether raw must be wrapped in quotes for safe
// YAML plain-style emission. Returns true for empty strings, strings with
// leading whitespace, strings containing any YAML-meta character, strings
// containing C0/DEL control characters (except TAB) per YAML 1.2 §5.1,
// strings with leading block/mapping indicators, strings with trailing
// whitespace, and document marker strings.
func needsQuoting(raw string) bool {
	if raw == "" {
		return true
	}
	// Leading whitespace
	if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") {
		return true
	}
	// YAML 1.2 §6.3.5 plain-style indicators: `-` / `?` / `:` are flow /
	// explicit-key / mapping-value indicators when followed by space, tab, EOL,
	// or when they are the entire scalar. Quoting forces literal scalar
	// interpretation and prevents the value from being parsed as a block
	// sequence entry, explicit mapping key, or mapping value indicator.
	if len(raw) >= 1 {
		first := raw[0]
		if first == '-' || first == '?' || first == ':' {
			if len(raw) == 1 || raw[1] == ' ' || raw[1] == '\t' || raw[1] == '\n' || raw[1] == '\r' {
				return true
			}
		}
	}
	// Trailing whitespace is stripped from plain scalars by yaml.v3, so a
	// value ending in space or tab would silently lose characters on round-trip.
	if last := raw[len(raw)-1]; last == ' ' || last == '\t' {
		return true
	}
	// Document marker lines must be quoted to prevent consumer parsers from
	// treating the scalar value as a document boundary or end-of-document marker.
	if raw == "---" || raw == "..." {
		return true
	}
	// YAML 1.2 §5.1 — C0/DEL control characters (except TAB) cannot appear
	// in scalars without escaping; route through doubleQuote so they round-trip.
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b < 0x20 && b != '\t' {
			return true
		}
		if b == 0x7f {
			return true
		}
	}
	// YAML-meta characters: colon, braces, brackets, comma, special indicators,
	// newlines and NUL. Single-quote included so embedded quotes are doubled.
	if strings.ContainsAny(raw, ":{}[],&*#?|>!%@`\"'\n\r\x00") {
		return true
	}
	return false
}
