// Package scaffoldid is the single source of truth for scaffold identifier
// validation across cmd/gocell/app, kernel/assembly, and tools/codegen/cellgen.
//
// # Design
//
// ScaffoldID is a string newtype produced exclusively by Parse, which delegates
// the pattern check to kernel/metadata.MatchAssemblyID (^[a-z][a-z0-9]+$). The
// same pattern governs assembly and cell IDs in YAML metadata (FMT-A1), so
// scaffold input validation never reimplements the regex — single source.
//
// # AI-Hard funnel (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01)
//
// All consumers (AssemblyScaffoldSpec.ID, ScaffoldSpec.ID, cmd flag bindings)
// declare their identifier field as ScaffoldID, so a bare string cannot reach
// scaffold construction without flowing through Parse. The dual guard:
//
//  1. Compile-time: a string literal or *string flag value used directly as a
//     ScaffoldID is a type mismatch.
//  2. Archtest SCAFFOLD-ID-FUNNEL-01: scans all .go files outside this package
//     for `scaffoldid.ScaffoldID(...)` explicit conversions; the only
//     authorized cast site is Parse itself.
package scaffoldid

import (
	"log/slog"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ScaffoldID is a validated scaffold identifier — an assembly ID, a cell ID,
// or a structurally equivalent name for any other top-level scaffold artifact.
// The empty zero value is INVALID and must not be propagated; obtain a
// ScaffoldID through Parse.
type ScaffoldID string

// String returns the underlying string so the typed identifier can be used
// directly as a yaml/text scalar or in fmt-style formatting without an
// explicit cast at the consumer.
func (id ScaffoldID) String() string { return string(id) }

// Parse validates raw against kernel/metadata.AssemblyIDPattern (the shared
// no-dash concatenation rule used by both AssemblyID and CellID) and returns
// the typed ScaffoldID. Invalid input — empty, mixed case, leading digit,
// dashes, underscores, path separators, control characters, trailing
// whitespace, etc. — yields ErrValidationFailed with the pattern surfaced in
// the public details so CLI users see the exact constraint they missed.
func Parse(raw string) (ScaffoldID, error) {
	if !metadata.MatchAssemblyID(raw) {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffoldid: identifier does not match metadata AssemblyIDPattern",
			errcode.WithDetails(
				slog.String("id", raw),
				slog.String("pattern", metadata.AssemblyIDPattern),
			))
	}
	return ScaffoldID(raw), nil
}
