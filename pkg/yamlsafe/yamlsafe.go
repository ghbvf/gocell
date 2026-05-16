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

import "strings"

// Scalar is a typed YAML scalar that safely round-trips through plain-style
// YAML emission. Use Quote to construct from raw user input; the type system
// then prevents callers from passing raw strings to scaffold templates.
type Scalar string

// String returns the rendered scalar for template emission.
func (s Scalar) String() string { return string(s) }

// Quote constructs a Scalar from raw user input. If raw contains characters
// that cannot safely render as plain-style YAML (newline / NUL / leading
// whitespace / : { } [ ] , & * # ? | > ! % @ ` " '), the result is
// single-quoted with embedded single quotes doubled. Strings containing
// newlines or NUL use double-quoted form with escape sequences so they
// round-trip correctly through yaml.Unmarshal.
//
// This is the single funnel for YAML scalar quoting in scaffold templates.
func Quote(raw string) Scalar {
	if !needsQuoting(raw) {
		return Scalar(raw)
	}
	// Strings with newlines or NUL cannot be faithfully represented in
	// single-quoted YAML scalars (yaml.v3 folds embedded newlines). Use
	// double-quoted form with Go-style escape sequences instead.
	if strings.ContainsAny(raw, "\n\r\x00") {
		return Scalar(doubleQuote(raw))
	}
	// single-quoted YAML scalar; embedded single quotes doubled
	escaped := strings.ReplaceAll(raw, "'", "''")
	return Scalar("'" + escaped + "'")
}

// doubleQuote returns a YAML double-quoted scalar for raw, escaping
// backslashes, double-quotes, newlines, carriage-returns, and NUL bytes.
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
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// needsQuoting reports whether raw must be wrapped in quotes for safe
// YAML plain-style emission. Returns true for empty strings, strings with
// leading whitespace, or strings containing any YAML-meta character.
func needsQuoting(raw string) bool {
	if raw == "" {
		return true
	}
	// Leading whitespace
	if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") {
		return true
	}
	// YAML-meta characters: colon, braces, brackets, comma, special indicators,
	// newlines and NUL. Single-quote included so embedded quotes are doubled.
	if strings.ContainsAny(raw, ":{}[],&*#?|>!%@`\"'\n\r\x00") {
		return true
	}
	return false
}
