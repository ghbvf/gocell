package governance

import (
	"context"
	"fmt"
	"strings"
)

// ValidateStrict runs all standard validation rules and, when strict is true,
// additionally enforces the following strict-only checks as errors:
//
//   - FMT-16: slice / cell / assembly directory contains '-' (kebab-case disallowed)
//   - FMT-17: slice.yaml allowedFiles first entry does not match the slice directory
//   - FMT-18: wrapper.ContractSpec literals in cells/** disagree with contracts/**/contract.yaml
//   - FMT-19: kernel/wrapper/*.go contains forbidden mutable package-level state
//   - VERIFY-06: active journeys have at least one auto passCriteria checkRef
//   - FMT-C1: cell.yaml id contains '-' (kebab-case cell id disallowed)
//   - FMT-A1: assembly.yaml id contains '-' (kebab-case assembly id disallowed)
//   - DOC-NAME-01: active docs contain a forbidden legacy naming literal
//
// When strict is false the method is equivalent to Validate(ctx) —
// strict-only rules emit nothing (they are strict-only by design, there is
// no warning severity to "upgrade" from). ctx flows into VERIFY-06 because
// it shells out via verifyJourneyRef to run journey acceptance tests; the
// remaining strict-only rules are pure-memory.
//
// ctx cancellation is checked between strict-only rules so a worker that
// aborts the validate command unwinds the strict pass too — not just the
// base Validate pipeline.
func (v *Validator) ValidateStrict(ctx context.Context, strict bool) ([]ValidationResult, error) {
	results, err := v.Validate(ctx)
	if err != nil {
		return results, err
	}
	for _, rule := range v.strictRules(ctx, strict) {
		if cerr := ctx.Err(); cerr != nil {
			return results, cerr
		}
		results = append(results, rule()...)
	}
	return results, nil
}

// ValidateStrictFailFast is equivalent to ValidateStrict(ctx, true) but uses
// ValidateFailFast as its base pass instead of Validate. The base pass
// short-circuits on the first SeverityError; strict-only rules are only
// appended when the base pass finds no errors. Rules are appended
// incrementally; as soon as any rule produces an error the accumulation
// stops, matching --strict --fail-fast's single-error semantics.
//
// ctx cancellation is checked between strict-only rules so a CI worker that
// aborts the validate command unwinds the strict pass too — not just the
// base Validate pipeline.
func (v *Validator) ValidateStrictFailFast(ctx context.Context) ([]ValidationResult, error) {
	results, err := v.ValidateFailFast(ctx)
	if err != nil {
		return results, err
	}
	if HasErrors(results) {
		return results, nil
	}
	for _, rule := range v.strictRules(ctx, true) {
		if cerr := ctx.Err(); cerr != nil {
			return results, cerr
		}
		r := rule()
		results = append(results, r...)
		if HasErrors(r) {
			return results, nil
		}
	}
	return results, nil
}

// strictRules returns the strict-only rule pipeline as zero-arg closures so
// ValidateStrict and ValidateStrictFailFast share a single ctx.Err() loop.
// VERIFY-06 binds ctx via the closure (it shells out via verifyJourneyRef);
// the remaining FMT / DOC rules are pure-memory and accept only the strict
// flag, so the closures are trivial.
func (v *Validator) strictRules(ctx context.Context, strict bool) []func() []ValidationResult {
	return []func() []ValidationResult{
		func() []ValidationResult { return v.validateVERIFY06(ctx, strict) },
		func() []ValidationResult { return v.validateFMT16(strict) },
		func() []ValidationResult { return v.validateFMT17(strict) },
		func() []ValidationResult { return v.validateFMT18(strict) },
		func() []ValidationResult { return v.validateFMT19(strict) },
		func() []ValidationResult { return v.validateFMTC1(strict) },
		func() []ValidationResult { return v.validateFMTA1(strict) },
		func() []ValidationResult { return v.validateDOCNAME01(strict) },
	}
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
	for _, s := range v.project.Slices {
		results = append(results, v.checkKebabDir(s.Dir, s.ID, sliceFile(s), "slice")...)
	}
	for _, c := range v.project.Cells {
		results = append(results, v.checkKebabDir(c.Dir, c.ID, cellFile(c), "cell")...)
	}
	for _, a := range v.project.Assemblies {
		results = append(results, v.checkKebabDir(a.Dir, a.ID, assemblyFile(a), "assembly")...)
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
	for _, s := range v.project.Slices {
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
				sliceFile(s),
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
			cellFile(c),
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
			assemblyFile(a),
			"id",
			fmt.Sprintf(
				"assembly id %q contains '-'; kebab-case assembly ids are disallowed in strict mode (rename to %q)",
				a.ID, strings.ReplaceAll(a.ID, "-", ""),
			),
		))
	}
	return results
}
