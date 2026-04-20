package governance

import (
	"fmt"
	"strings"
)

// ValidateStrict runs all standard validation rules and, when strict is true,
// additionally enforces the following strict-only checks as errors:
//
//   - FMT-16: slice directory contains '-' (kebab-case disallowed in strict mode)
//   - FMT-17: slice.yaml allowedFiles first entry does not match the slice directory
//
// When strict is false the method is equivalent to Validate() — FMT-16 and
// FMT-17 emit nothing (they are strict-only rules; non-strict mode is silent
// by design, there is no warning severity to "upgrade" from).
func (v *Validator) ValidateStrict(strict bool) []ValidationResult {
	results := v.Validate()
	results = append(results, v.validateFMT16(strict)...)
	results = append(results, v.validateFMT17(strict)...)
	return results
}

// ValidateStrictFailFast is equivalent to ValidateStrict(true) but uses
// ValidateFailFast as its base pass instead of Validate. The base pass
// short-circuits on the first SeverityError; FMT-16 and FMT-17 are only
// appended when the base pass finds no errors. This gives --strict --fail-fast
// true single-error semantics: if any standard rule fires, FMT-16/17 are
// skipped entirely.
func (v *Validator) ValidateStrictFailFast() []ValidationResult {
	results := v.ValidateFailFast()
	if HasErrors(results) {
		return results
	}
	results = append(results, v.validateFMT16(true)...)
	if HasErrors(results) {
		return results
	}
	results = append(results, v.validateFMT17(true)...)
	return results
}

// validateFMT16 checks that no slice directory contains '-' (kebab-case).
// In strict mode this is a SeverityError; in non-strict mode it is silent.
//
// The check reads the filesystem directory segment captured by the parser
// (SliceMeta.Dir), not the map key or slice.id. This matters: a slice can
// live under a kebab directory while declaring a no-dash id in slice.yaml,
// and pre-Dir implementations that read the map key saw only the id and let
// the kebab directory slip through. Slices synthesized in tests without a
// Dir are skipped (Dir != "" is the "parsed from disk" signal).
func (v *Validator) validateFMT16(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	var results []ValidationResult
	for key, s := range v.project.Slices {
		if s.Dir == "" {
			continue
		}
		if strings.Contains(s.Dir, "-") {
			results = append(results, v.newResult(
				"FMT-16", SeverityError, IssueInvalid,
				sliceFile(key),
				"id",
				fmt.Sprintf(
					"slice %q uses kebab-case directory %q; kebab-case slice directories are disallowed in strict mode (rename to %q)",
					s.ID, s.Dir, strings.ReplaceAll(s.Dir, "-", ""),
				),
			))
		}
	}
	return results
}

// validateFMT17 checks that the first entry in slice.yaml allowedFiles matches
// the canonical slice directory path. In strict mode this is a SeverityError;
// in non-strict mode it is silent. Expected path is derived from SliceMeta.Dir
// / CellDir (filesystem truth) so a faked-path/faked-id pairing cannot slip
// through.
func (v *Validator) validateFMT17(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	var results []ValidationResult
	for key, s := range v.project.Slices {
		if len(s.AllowedFiles) == 0 {
			// FMT-14 already covers missing allowedFiles; skip here.
			continue
		}
		if s.Dir == "" || s.CellDir == "" {
			continue
		}
		expected := fmt.Sprintf("cells/%s/slices/%s/", s.CellDir, s.Dir)
		first := s.AllowedFiles[0]
		// Normalize: strip trailing ** or glob suffix for comparison.
		normalized := strings.TrimSuffix(first, "**")
		normalized = strings.TrimSuffix(normalized, "*")
		if !strings.HasPrefix(normalized, expected) && normalized != expected {
			results = append(results, v.newResult(
				"FMT-17", SeverityError, IssueMismatch,
				sliceFile(key),
				"allowedFiles[0]",
				fmt.Sprintf(
					"slice %q allowedFiles first entry %q does not match slice directory %q (want prefix %q)",
					s.ID, first, s.Dir, expected,
				),
			))
		}
	}
	return results
}
