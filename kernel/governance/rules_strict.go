package governance

import (
	"fmt"
	"strings"
)

// ValidateStrict runs all standard validation rules and, when strict is true,
// additionally upgrades the following advisory checks to errors:
//
//   - FMT-16: slice directory contains '-' (kebab-case disallowed in strict mode)
//   - FMT-17: slice.yaml allowedFiles first entry does not match the slice directory
//
// When strict is false the method is equivalent to Validate() — FMT-16 and
// FMT-17 produce warnings only (they are omitted entirely in the standard
// rules because they only apply to strict mode context).
func (v *Validator) ValidateStrict(strict bool) []ValidationResult {
	results := v.Validate()
	results = append(results, v.validateFMT16(strict)...)
	results = append(results, v.validateFMT17(strict)...)
	return results
}

// validateFMT16 checks that no slice key contains '-' (kebab-case directory).
// In strict mode this is a SeverityError; in non-strict mode it is silent.
func (v *Validator) validateFMT16(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	var results []ValidationResult
	for key, s := range v.project.Slices {
		// key format: "cellID/sliceID"
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		sliceDir := parts[1]
		if strings.Contains(sliceDir, "-") {
			results = append(results, v.newResult(
				"FMT-16", SeverityError, IssueInvalid,
				sliceFile(key),
				"id",
				fmt.Sprintf(
					"slice %q uses kebab-case directory %q; kebab-case slice directories are disallowed in strict mode (rename to %q)",
					s.ID, sliceDir, strings.ReplaceAll(sliceDir, "-", ""),
				),
			))
		}
	}
	return results
}

// validateFMT17 checks that the first entry in slice.yaml allowedFiles matches
// the canonical slice directory path. In strict mode this is a SeverityError;
// in non-strict mode it is silent.
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
		// key format: "cellID/sliceID"
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		cellID := parts[0]
		sliceDir := parts[1]
		expected := fmt.Sprintf("cells/%s/slices/%s/", cellID, sliceDir)
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
					s.ID, first, sliceDir, expected,
				),
			))
		}
	}
	return results
}
