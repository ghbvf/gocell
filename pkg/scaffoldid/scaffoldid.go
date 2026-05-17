// Package scaffoldid is the single source of truth for typed scaffold
// identifier validation across the project.
//
// # Layer
//
// pkg/ — depends only on stdlib + pkg/errcode. Reachable from every layer
// (kernel/, runtime/, cells/, adapters/, cmd/) without crossing the
// layer-dependency rules. Domain spec fields (e.g.
// kernel/assembly.AssemblyScaffoldSpec.ID,
// tools/codegen/cellgen.ScaffoldSpec.CellID) declare ScaffoldID, and the
// CLI layer (cmd/gocell/app) parses raw flag strings through Parse at the
// boundary — mirroring k8s.io/apimachinery/pkg/types typed-identifier
// pattern (NamespacedName / UID in pkg-style helper packages; CLI parses
// at the boundary).
//
// # Design
//
// ScaffoldID is a struct newtype (NOT a string newtype) with one unexported
// field. The only public constructor is Parse, which validates the input
// against IdentifierPattern (`^[a-z][a-z0-9]+$`). The same pattern is the
// single source for kernel/metadata.AssemblyIDPattern / CellIDPattern via a
// reverse alias (metadata re-exports the const). YAML schemas under
// kernel/metadata/schemas/ continue to carry literal regex strings, and
// kernel/metadata/schemas TestSchemaConstants asserts schema literals match
// the const byte-for-byte.
//
// # AI-Hard funnel (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01)
//
// The struct-with-unexported-field shape is a compile-time Hard funnel:
//
//  1. `var x scaffoldid.ScaffoldID = "raw"` — fails to compile (untyped
//     string const cannot convert to a struct).
//  2. `scaffoldid.ScaffoldID("raw")` explicit conversion — fails to compile
//     (struct types do not accept conversion from string).
//  3. `[]scaffoldid.ScaffoldID{"a", "b"}` slice literal with string elements
//     — fails to compile for the same reason as (1).
//  4. `scaffoldid.ScaffoldID{value: "raw"}` struct literal — fails to compile
//     outside this package (field is unexported).
//
// The ONLY way to obtain a non-zero ScaffoldID is through Parse. No archtest
// is required for the construction funnel; the Go type system enforces it.
//
// Tests construct fixtures via a package-internal mustID(t, raw) helper that
// calls Parse and t.Fatal on validation failure; the package exposes NO
// MustParse / panic-based shortcut, eliminating the public backdoor that
// the funnel claim would otherwise contradict.
package scaffoldid

import (
	"log/slog"
	"regexp"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// IdentifierPattern is the single-source regex for typed scaffold identifiers
// — lowercase ASCII letters + digits, ≥2 chars, must start with a letter.
// Re-exported by kernel/metadata (AssemblyIDPattern / CellIDPattern) so YAML
// schema validators consume the same pattern.
const IdentifierPattern = `^[a-z][a-z0-9]+$`

var identifierRe = regexp.MustCompile(IdentifierPattern)

// Match reports whether s satisfies IdentifierPattern. Predicate-style
// helper for callers that don't need a typed ScaffoldID — typically YAML
// schema validators in kernel/metadata.
func Match(s string) bool { return identifierRe.MatchString(s) }

// ScaffoldID is a validated scaffold identifier — an assembly ID, a cell ID,
// or a structurally equivalent name for any other top-level scaffold artifact.
// The zero value (ScaffoldID{}) is INVALID and must not be propagated; obtain
// a ScaffoldID through Parse. The struct-with-unexported-field shape makes
// cross-package construction unrepresentable at the type level.
type ScaffoldID struct {
	value string
}

// String returns the underlying string so the typed identifier can be used
// directly as a yaml/text scalar or in fmt-style formatting without an
// explicit unwrap at the consumer. Implements fmt.Stringer so text/template
// renders `{{.CellID}}` as the value.
func (id ScaffoldID) String() string { return id.value }

// IsZero reports whether id is the zero value (no value set via Parse).
// Constructors / validators use this to reject ScaffoldSpec{} that bypassed
// flag binding.
func (id ScaffoldID) IsZero() bool { return id.value == "" }

// Parse validates raw against IdentifierPattern and returns the typed
// ScaffoldID. Invalid input — empty, mixed case, leading digit, dashes,
// underscores, path separators, control characters, trailing whitespace,
// etc. — yields ErrValidationFailed with the pattern surfaced in the public
// details so CLI users see the exact constraint they missed.
func Parse(raw string) (ScaffoldID, error) {
	if !Match(raw) {
		return ScaffoldID{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffoldid: identifier does not match IdentifierPattern",
			errcode.WithDetails(
				slog.String("id", raw),
				slog.String("pattern", IdentifierPattern),
				slog.String("hint", "lowercase letters and digits only, at least 2 chars, no dashes / underscores / dots"),
			))
	}
	return ScaffoldID{value: raw}, nil
}
