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
// archtest SCAFFOLD-WRITE-FUNNEL-01 (path safety) and in GREEN phase will be
// covered by YAML-QUOTE-FUNNEL-01.
package yamlsafe

// Scalar is a typed YAML scalar that safely round-trips through plain-style
// YAML emission. Use Quote to construct from raw user input; the type system
// then prevents callers from passing raw strings to scaffold templates.
type Scalar string

// String returns the rendered scalar for template emission.
func (s Scalar) String() string { return string(s) }

// Quote constructs a Scalar from raw user input. If raw contains characters
// that cannot safely render as plain-style YAML (newline / NUL / leading
// whitespace / : { } [ ] , & * # ? | > ! % @ ` " '), the result is
// single-quoted with embedded single quotes doubled.
//
// This is the single funnel for YAML scalar quoting in scaffold templates.
//
// RED stub: returns "" for all input. GREEN phase will implement quoting logic.
func Quote(raw string) Scalar { return "" }
