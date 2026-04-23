package governance

import (
	"fmt"
	"strings"
)

// ValidateStrict runs all standard validation rules and, when strict is true,
// additionally enforces the following strict-only checks as errors:
//
//   - FMT-16: slice / cell / assembly directory contains '-' (kebab-case disallowed)
//   - FMT-17: slice.yaml allowedFiles first entry does not match the slice directory
//   - FMT-C1: cell.yaml id contains '-' (kebab-case cell id disallowed)
//   - FMT-A1: assembly.yaml id contains '-' (kebab-case assembly id disallowed)
//
// When strict is false the method is equivalent to Validate() — strict-only
// rules emit nothing (they are strict-only by design, there is no warning
// severity to "upgrade" from).
func (v *Validator) ValidateStrict(strict bool) []ValidationResult {
	results := v.Validate()
	results = append(results, v.validateFMT16(strict)...)
	results = append(results, v.validateFMT17(strict)...)
	results = append(results, v.validateFMTC1(strict)...)
	results = append(results, v.validateFMTA1(strict)...)
	return results
}

// ValidateStrictFailFast is equivalent to ValidateStrict(true) but uses
// ValidateFailFast as its base pass instead of Validate. The base pass
// short-circuits on the first SeverityError; strict-only rules are only
// appended when the base pass finds no errors. Rules are appended
// incrementally; as soon as any rule produces an error the accumulation
// stops, matching --strict --fail-fast's single-error semantics.
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
	if HasErrors(results) {
		return results
	}
	results = append(results, v.validateFMTC1(true)...)
	if HasErrors(results) {
		return results
	}
	results = append(results, v.validateFMTA1(true)...)
	return results
}

// validateFMT16 checks that no slice, cell, or assembly directory contains
// '-' (kebab-case). In strict mode this is a SeverityError; in non-strict
// mode it is silent.
//
// The check reads the filesystem directory segment captured by the parser
// (SliceMeta.Dir / CellMeta.Dir / AssemblyMeta.Dir), not the map key or
// yaml id. This matters: a directory can live under a kebab name while
// declaring a no-dash id in yaml, and pre-Dir implementations that read
// only the id let kebab directories slip through. Entries synthesized in
// tests without a Dir are skipped (Dir != "" is the "parsed from disk"
// signal).
func (v *Validator) validateFMT16(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	var results []ValidationResult
	for key, s := range v.project.Slices {
		results = append(results, v.checkKebabDir(s.Dir, s.ID, sliceFile(key), "slice")...)
	}
	for _, c := range v.project.Cells {
		results = append(results, v.checkKebabDir(c.Dir, c.ID, cellFile(c.ID), "cell")...)
	}
	for _, a := range v.project.Assemblies {
		results = append(results, v.checkKebabDir(a.Dir, a.ID, assemblyFile(a.ID), "assembly")...)
	}
	return results
}

// checkKebabDir returns a FMT-16 error if dir is non-empty and contains '-'.
// kind is one of "slice", "cell", "assembly" — used only in the error message.
func (v *Validator) checkKebabDir(dir, id, file, kind string) []ValidationResult {
	if dir == "" || !strings.Contains(dir, "-") {
		return nil
	}
	return []ValidationResult{v.newResult(
		"FMT-16", SeverityError, IssueInvalid,
		file,
		"id",
		fmt.Sprintf(
			"%s %q uses kebab-case directory %q; kebab-case %s directories are disallowed in strict mode (rename to %q)",
			kind, id, dir, kind, strings.ReplaceAll(dir, "-", ""),
		),
	)}
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
		if s.File != "" {
			expected = strings.TrimSuffix(s.File, "slice.yaml")
		}
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

// validateFMTC1 checks that no cell.yaml declares a kebab-case id (contains '-').
// In strict mode this is a SeverityError; in non-strict mode it is silent.
//
// This complements FMT-16: FMT-16 catches kebab filesystem directories, while
// FMT-C1 catches kebab yaml ids even when the directory is already no-dash
// (e.g. during a migration where id is fixed last). A clean project passes
// both; a half-migrated project typically trips one of them.
func (v *Validator) validateFMTC1(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if !strings.Contains(c.ID, "-") {
			continue
		}
		results = append(results, v.newResult(
			"FMT-C1", SeverityError, IssueInvalid,
			cellFile(c.ID),
			"id",
			fmt.Sprintf(
				"cell id %q contains '-'; kebab-case cell ids are disallowed in strict mode (rename to %q)",
				c.ID, strings.ReplaceAll(c.ID, "-", ""),
			),
		))
	}
	return results
}

// validateFMTA1 checks that no assembly.yaml declares a kebab-case id. In
// strict mode this is a SeverityError; in non-strict mode it is silent.
//
// Mirrors FMT-C1 for assemblies. Assembly ids are referenced by binary name
// and deploy templates, so a kebab id leaks into filesystem and CI artifacts.
func (v *Validator) validateFMTA1(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		if !strings.Contains(a.ID, "-") {
			continue
		}
		results = append(results, v.newResult(
			"FMT-A1", SeverityError, IssueInvalid,
			assemblyFile(a.ID),
			"id",
			fmt.Sprintf(
				"assembly id %q contains '-'; kebab-case assembly ids are disallowed in strict mode (rename to %q)",
				a.ID, strings.ReplaceAll(a.ID, "-", ""),
			),
		))
	}
	return results
}
