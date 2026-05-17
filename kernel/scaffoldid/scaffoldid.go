// Package scaffoldid is the single source of truth for scaffold identifier
// validation across cmd/gocell/app, kernel/assembly, and tools/codegen/cellgen.
//
// # Design
//
// ScaffoldID is a struct newtype (NOT a string newtype) with one unexported
// field. The only constructor is Parse, which delegates the pattern check
// to kernel/metadata.MatchAssemblyID (^[a-z][a-z0-9]+$). The same pattern
// governs assembly and cell IDs in YAML metadata (FMT-A1), so scaffold input
// validation never reimplements the regex — single source.
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
package scaffoldid

import (
	"log/slog"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

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

// Parse validates raw against kernel/metadata.AssemblyIDPattern (the shared
// no-dash concatenation rule used by both AssemblyID and CellID) and returns
// the typed ScaffoldID. Invalid input — empty, mixed case, leading digit,
// dashes, underscores, path separators, control characters, trailing
// whitespace, etc. — yields ErrValidationFailed with the pattern surfaced in
// the public details so CLI users see the exact constraint they missed.
func Parse(raw string) (ScaffoldID, error) {
	if !metadata.MatchAssemblyID(raw) {
		return ScaffoldID{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffoldid: identifier does not match metadata AssemblyIDPattern",
			errcode.WithDetails(
				slog.String("id", raw),
				slog.String("pattern", metadata.AssemblyIDPattern),
				slog.String("hint", "lowercase letters and digits only, at least 2 chars, no dashes / underscores / dots"),
			))
	}
	return ScaffoldID{value: raw}, nil
}

// MustParse is like Parse but panics if raw fails validation. Intended for
// test fixtures and package-level variable initialization where the input is
// a known-good literal constant (mirror of regexp.MustCompile / template.Must
// in the standard library).
//
// Production code MUST NOT call MustParse — use Parse and propagate the
// validation error through the CLI / API boundary. archtest enforces this:
// MustParse callsites outside *_test.go files would be a Hard funnel breach.
func MustParse(raw string) ScaffoldID {
	id, err := Parse(raw)
	if err != nil {
		panic(panicregister.Approved("scaffoldid-mustparse",
			errcode.Assertion("scaffoldid.MustParse: validation failed")))
	}
	return id
}
