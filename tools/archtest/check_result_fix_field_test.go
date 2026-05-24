// INVARIANT: CMD-VALIDATIONRESULT-FIX-FIELD-01
//
// CMD-VALIDATIONRESULT-FIX-FIELD-01 verifies that every governance.ValidationResult{}
// composite literal in cmd/gocell/app carries a non-empty, resolvable Fix: field.
//
// This invariant is severity-agnostic: all ValidationResult literals — error and
// warning — must provide structured remediation guidance via the typed Fix field,
// matching the funnel contract established for kernel/governance by INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01).
//
// The cmd/gocell/app package constructs ValidationResult literals directly (not
// through kernel/governance's internal locator constructors) because those
// constructors are unexported. cmd-side literals are therefore outside the
// kernel/governance funnel and require an independent archtest guard.
//
// AI-robust grading (Funnel 双向锁):
//   - Downstream Hard: Fix field non-empty and resolvable — archtest statically
//     rejects any composite literal where Fix is absent, empty string, or
//     unresolvable to literal content. Go cannot type-enforce non-empty string at
//     compile time, so archtest is the tightest achievable guard ("typed marker
//     funnel for unbounded ops" ceiling from ai-robust.md).
//   - Upstream Medium: The scan targets composite literal AST nodes inside
//     cmd/gocell/app. Go's type system cannot compile-reject an in-package
//     literal that omits Fix; archtest catches it at CI time. Upstream Hard
//     hardening (sealing ValidationResult fields so pkg-external literal
//     construction without Fix is unexpressible) is tracked by gh issue #922
//     (orthogonal, ~700 field-read sites); not in scope for this PR.
//
// Cross-reference: mirrors INV-3 (GOVERNANCE-RULE-ERROR-FIX-FIELD-01) in
// governance_rules_invariants_test.go. The same resolveStringFragments /
// fixHasLiteralContent helpers are reused here. The key difference from INV-3:
//   - INV-3 scans within kernel/governance and identifies ValidationResult
//     composites by name-match (the composites are intra-package, so
//     isValidationResultCompositeLit's pkgPath == governancePkgPath constraint
//     is satisfied by the package itself being governance).
//   - This test scans cmd/gocell/app (a cross-package consumer) and relies on
//     go/types to resolve the composite literal's type to
//     github.com/ghbvf/gocell/kernel/governance.ValidationResult — a stronger
//     type-gated approach since the type is imported, not declared in scope.
//
// Blind-spot self-check (AST forms outside the chosen tools' declared scope):
//   - ValidationResult returned from a helper function rather than constructed
//     as a composite literal would not be caught by this scan. The reverse
//     self-check is the production scan staying green: cmd/gocell/app currently
//     constructs all ValidationResult values as direct composite literals; no
//     helper-return indirection exists.
//   - ValidationResult built via reflection or interface conversion would evade
//     isValidationResultCompositeLit. No such path exists in cmd/gocell/app.
//   - A Fix: field set to a non-string-constant expression (e.g. a variable
//     forwarded from a parameter) would be flagged as unresolvable by
//     resolveStringFragments returning nil, which fixHasLiteralContent maps to
//     false — violation reported. This is fail-closed: unknown = violation.
//   - fmt.Sprintf("%s", someVar) templates are caught by fixHasLiteralContent:
//     after verb stripping, no literal text remains → violation reported.
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestCmdValidationResultFixField verifies CMD-VALIDATIONRESULT-FIX-FIELD-01:
// every governance.ValidationResult{} composite literal in cmd/gocell/app must
// carry a non-empty, resolvable Fix: field.
//
// Sub-tests:
//   - negative_fixtures_caught: proves both scan paths (missing Fix, empty Fix)
//     are genuinely active via testdata fixture packages.
//   - production_source_all_pass: verifies cmd/gocell/app currently has zero
//     violations (GREEN baseline).
func TestCmdValidationResultFixField(t *testing.T) {
	t.Run("negative_fixtures_caught", testCMDFixFieldNegativeFixtures)
	t.Run("production_source_all_pass", testCMDFixFieldProductionSource)
}

// testCMDFixFieldNegativeFixtures proves the scan is genuinely active.
// Each fixture package contains a governance.ValidationResult{} composite
// literal that violates the Fix field rule. The test asserts that at least one
// violation is reported for each fixture.
func testCMDFixFieldNegativeFixtures(t *testing.T) {
	cases := []struct {
		pattern string
		wantMin int
		shape   string
	}{
		{
			pattern: "./tools/archtest/testdata/cmd_validation_result_fix_fixtures/missing_fix_red",
			wantMin: 1,
			shape:   "ValidationResult composite literal with Fix: field absent",
		},
		{
			pattern: "./tools/archtest/testdata/cmd_validation_result_fix_fixtures/empty_fix_red",
			wantMin: 1,
			shape:   "ValidationResult composite literal with Fix: \"\" (empty literal)",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.shape, func(t *testing.T) {
			var violations []string
			RunTypedFixture(t, FixtureOpts{Tests: false}, []string{tc.pattern},
				func(p *Pass) []Diagnostic {
					consts := collectPackageStringConsts(p.Pkg.Scope())
					for _, file := range p.Files {
						violations = append(violations,
							scanCmdFixFieldViolationsInFile(file, p.Fset, p.TypesInfo, consts, p.Rel(file))...)
					}
					return nil
				})

			assert.GreaterOrEqual(t, len(violations), tc.wantMin,
				"shape %q: expected at least %d CMD-VALIDATIONRESULT-FIX-FIELD-01 violation(s), got %d: %v",
				tc.shape, tc.wantMin, len(violations), violations)
		})
	}
}

// testCMDFixFieldProductionSource verifies the production cmd/gocell/app package
// passes CMD-VALIDATIONRESULT-FIX-FIELD-01: every governance.ValidationResult{}
// composite literal carries a non-empty, resolvable Fix: field.
func testCMDFixFieldProductionSource(t *testing.T) {
	var violations []string
	RunTyped(t, TypedOpts{Tests: false}, []string{"./cmd/gocell/app"},
		func(p *Pass) []Diagnostic {
			consts := collectPackageStringConsts(p.Pkg.Scope())
			for _, file := range p.Files {
				violations = append(violations,
					scanCmdFixFieldViolationsInFile(file, p.Fset, p.TypesInfo, consts, p.Rel(file))...)
			}
			return nil
		})

	sort.Strings(violations)
	assert.Empty(t, violations,
		"every governance.ValidationResult{} composite literal in cmd/gocell/app "+
			"must carry a non-empty resolvable Fix: field (CMD-VALIDATIONRESULT-FIX-FIELD-01)")
}

// scanCmdFixFieldViolationsInFile reports all CMD-VALIDATIONRESULT-FIX-FIELD-01
// violations in a single AST file.
//
// Scan logic: every governance.ValidationResult{} composite literal in the file
// must satisfy all three conditions:
//  1. All fields are named (no positional elements) — positional elements skip
//     the Fix: key check entirely.
//  2. A Fix: field key is explicitly present.
//  3. The Fix: field value resolves to a non-empty string with literal content
//     (using the same resolveStringFragments / fixHasLiteralContent helpers as
//     INV-3 so the two invariants are consistent).
//
// The type gate uses p.TypesInfo to identify composite literals typed as
// github.com/ghbvf/gocell/kernel/governance.ValidationResult — this is a
// stronger cross-package type resolution compared to INV-3's same-package scan.
//
// pkgPath is always governancePkgPath — the type we are looking for is always
// governance.ValidationResult regardless of which package is being scanned.
func scanCmdFixFieldViolationsInFile(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	consts map[string]string,
	relPath string,
) []string {
	var violations []string

	scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		// Type gate: only governance.ValidationResult composite literals.
		if !isValidationResultCompositeLit(cl, info, governancePkgPath) {
			return
		}

		pos := fset.Position(cl.Pos())

		// (1) Positional ban: every element must be Key:Value. A positional
		// element bypasses the Fix: lookup entirely.
		keyValueCount := 0
		scanner.EachInChildren[ast.KeyValueExpr](cl, func(_ *ast.KeyValueExpr) {
			keyValueCount++
		})
		if len(cl.Elts) > 0 && keyValueCount != len(cl.Elts) {
			violations = append(violations,
				relPath+":"+strconv.Itoa(pos.Line)+
					": governance.ValidationResult literal must use named fields — "+
					"positional elements are forbidden because they skip the Fix: completeness check "+
					"(CMD-VALIDATIONRESULT-FIX-FIELD-01)")
			return
		}

		// (2) Fix: field must be present.
		var fixValue ast.Expr
		scanner.EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return
			}
			if key.Name == "Fix" {
				fixValue = kv.Value
			}
		})
		if fixValue == nil {
			violations = append(violations,
				relPath+":"+strconv.Itoa(pos.Line)+
					": governance.ValidationResult literal omits Fix: field — every result "+
					"(error or warning) must carry remediation guidance in the typed Fix field "+
					"(CMD-VALIDATIONRESULT-FIX-FIELD-01)")
			return
		}

		// (3) Fix: value must resolve to non-empty literal content.
		resolved := strings.Join(resolveStringFragments(fixValue, consts, info), "")
		if !fixHasLiteralContent(resolved) {
			violations = append(violations,
				relPath+":"+strconv.Itoa(pos.Line)+
					": governance.ValidationResult Fix: field is empty, unresolvable, or carries "+
					"no literal remediation guidance — every finding must have actionable Fix text "+
					"(CMD-VALIDATIONRESULT-FIX-FIELD-01)")
		}
	})

	return violations
}
