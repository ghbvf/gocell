package governance

import (
	"context"
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// ValidateStrict runs all standard validation rules and, when strict is true,
// additionally enforces the following strict-only checks as errors:
//
//   - FMT-16: slice / cell / assembly directory contains '-' (kebab-case disallowed)
//   - FMT-17: slice.yaml allowedFiles first entry does not match the slice directory
//   - FMT-19: kernel/wrapper/*.go contains forbidden mutable package-level state
//   - VERIFY-06: active journeys have at least one auto passCriteria checkRef
//   - FMT-C1: cell.yaml id contains '-' (kebab-case cell id disallowed)
//   - DOC-NAME-01: active docs contain a forbidden legacy naming literal
//
// FMT-A1 (assembly id pattern) is unconditional inside Validate: it
// mirrors schemas/assembly.schema.json id.pattern and must apply on every
// validate path so schema-aware tooling and `gocell validate` agree.
//
// FMT-18 (wrapper.ContractSpec literals in cells/** cross-check) was removed in
// PR-V1-CODEGEN-FULL-MIGRATION: after W3 cells/** has 0 ContractSpec literals,
// enforced by archtest CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01 /
// NO-MANUAL-CONTRACTSPEC-LITERAL-01 / EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01.
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
		// FMT-18 deleted in PR-V1-CODEGEN-FULL-MIGRATION W4 (replaced by archtest
		// CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01 / NO-MANUAL-CONTRACTSPEC-LITERAL-01).
		func() []ValidationResult { return v.validateFMT19(strict) },
		func() []ValidationResult { return v.validateFMTC1(strict) },
		// FMT-A1 is now registered in the default rules() pipeline (it
		// mirrors a schema constraint and applies on every validate path).
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

// validateFMTA1 checks that every assembly.yaml id satisfies
// metadata.AssemblyIDPattern (^[a-z][a-z0-9]+$). It runs unconditionally:
// the rule mirrors a schema-level constraint (schemas/assembly.schema.json
// properties.id.pattern, kept byte-equal by TestSchemaConstantsMatchSchema
// Literals) and schema-aware tooling rejects the same values without a
// strict toggle. Gating this check on --strict would leave default
// `gocell validate` users on a different contract than the schema and
// FMT-30 (deployTemplate enum), violating the single-gatekeeper model
// declared in docs/architecture/202605061800-adr-assembly-yaml-minimal-
// derivation.md §"Schema 约束单源".
//
// FMT-C1 / FMT-16 / FMT-17 stay strict-only because cell.schema.json id
// pattern itself permits kebab (different schema design); those rules
// remain stylistic strict-mode preferences, not schema mirrors.
//
// strict is accepted for signature symmetry with the strictRules block but
// no longer changes behavior.
func (v *Validator) validateFMTA1(_ bool) []ValidationResult {
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		if metadata.MatchAssemblyID(a.ID) {
			continue
		}
		results = append(results, v.newResult(
			"FMT-A1", SeverityError, IssueInvalid,
			assemblyFile(a),
			"id",
			fmt.Sprintf(
				"assembly id %q does not match %s; use lowercase ASCII letters + digits, ≥2 chars, starting with a letter",
				a.ID, metadata.AssemblyIDPattern,
			),
		))
	}
	return results
}
