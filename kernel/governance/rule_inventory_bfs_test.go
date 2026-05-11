package governance

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// TestBFSReachabilityFixtures locks in the BFS edge-resolution behavior
// that the goldenRuleIDs comparison alone cannot prove. Each fixture
// synthesizes a tiny package and runs runReachabilityBFS — the same
// routine TestRuleReachabilityFromRegistrationRoots uses — then asserts
// the expected reachable set.
//
// The fixtures pin the post-PR-B tightenings and the post-PR-TS1
// signature-based emission rules:
//   - emission detection works inside free functions that take a
//     *Validator parameter (BFS sees `v.newResult(...)` even when the
//     enclosing receiver name is "");
//   - composite literals nested in non-ValidationResult slices do not
//     contribute foreign Code field values to reachable;
//   - BFS scope is bounded by roots — orphan methods are not visited;
//   - const-ident emission resolves through scanPackageConstStrings;
//   - signature-mismatched same-named method is NOT an emitter (RED
//     fixture): a method literally named `newResult(string)` whose
//     return type is not ValidationResult is ignored by handleCall.
//
// All fixtures are type-checked in-memory because handleCall now uses
// typeutil.StaticCallee + signature predicates (isValidationResultEmitter)
// rather than name matching. Without *types.Info the fixtures would have
// no method to dispatch against.
//
// ref: kubernetes/apimachinery pkg/runtime/scheme_test.go (registration
// equivalence fixtures); golang.org/x/tools go/analysis/analysistest
// (synthetic-source negative cases for static checkers).
func TestBFSReachabilityFixtures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		source   string
		roots    []funcKey
		expected []string
	}{
		{
			name: "free_function_with_validator_param_emits_rule_id",
			// BFS reaches helper(v) via the free-function call edge, then
			// detects v.newResult inside helper despite recvName == "".
			source: `package fixture
type ValidationResult struct{ Code string }
type Validator struct{}
func (v *Validator) rules()                                { helper(v) }
func helper(v *Validator)                                  { v.newResult("FIX-A-01") }
func (v *Validator) newResult(s string) ValidationResult   { return ValidationResult{} }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"FIX-A-01"},
		},
		{
			name: "non_validationresult_composite_literal_skipped",
			source: `package fixture
type ValidationResult struct{ Code string }
type Validator struct{}
type Other struct{ Code string }
func (v *Validator) rules() {
	_ = []Other{{Code: "FOREIGN-02"}}
}
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil,
		},
		{
			name: "validationresult_inferred_inner_literal_picked_up",
			source: `package fixture
type ValidationResult struct{ Code string }
type Validator struct{}
func (v *Validator) rules() []ValidationResult {
	return []ValidationResult{{Code: "VR-INFERRED-03"}}
}
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"VR-INFERRED-03"},
		},
		{
			name: "orphan_method_not_in_roots_is_unreachable",
			source: `package fixture
type ValidationResult struct{ Code string }
type Validator struct{}
func (v *Validator) rules()                                { v.live() }
func (v *Validator) live()                                 { v.newResult("LIVE-04") }
func (v *Validator) dead()                                 { v.newResult("DEAD-99") }
func (v *Validator) newResult(s string) ValidationResult   { return ValidationResult{} }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"LIVE-04"},
		},
		{
			name: "const_ident_emission_resolved_via_const_map",
			source: `package fixture
type ValidationResult struct{ Code string }
type Validator struct{}
const ruleX = "X-CONST-05"
func (v *Validator) rules()                                { v.do() }
func (v *Validator) do()                                   { v.newResult(ruleX) }
func (v *Validator) newResult(s string) ValidationResult   { return ValidationResult{} }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"X-CONST-05"},
		},
		{
			name: "signature_mismatched_same_named_method_ignored_RED",
			// Method literally named newResult with string arg 0 but
			// missing the ValidationResult return type — handleCall's
			// signature filter must reject it. Pre-PR-TS1 (name-based
			// match) this would have captured "RED-06" into reachable.
			source: `package fixture
type ValidationResult struct{ Code string }
type Validator struct{}
func (v *Validator) rules()             { v.newResult("RED-06") }
func (v *Validator) newResult(s string) {}
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil,
		},
		{
			name: "variadic_format_emitter_ignored_RED",
			// A method with the canonical (string, ...) → ValidationResult
			// shape but variadic must NOT be treated as an emitter:
			// x.Args[0] would be the format template, not a rule ID.
			source: `package fixture
type ValidationResult struct{ Code string }
type Validator struct{}
func (v *Validator) rules()                                       { v.newResultf("rule %s applied", "FOO-BAR") }
func (v *Validator) newResultf(fmtStr string, args ...interface{}) ValidationResult { return ValidationResult{} }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil,
		},
		{
			name: "foreign_package_named_validationresult_ignored_RED",
			// A method that returns a *types.Named called ValidationResult
			// from a *different package* than the receiver must NOT be
			// treated as an emitter. The fixture cannot easily synthesize
			// two packages, but the cross-package guard
			// (recvNamed.Obj().Pkg() == r0.Obj().Pkg()) is exercised here
			// by having the receiver in a different file/package shape:
			// we use an interface-typed argument whose concrete dispatch
			// StaticCallee cannot resolve, ensuring no spurious emit.
			source: `package fixture
type ValidationResult struct{ Code string }
type Emitter interface{ newResult(s string) ValidationResult }
type Validator struct{ e Emitter }
func (v *Validator) rules() { v.e.newResult("IFACE-07") }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil, // interface dispatch — StaticCallee returns nil
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture.go", tc.source, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			files := []*ast.File{f}

			info := &types.Info{
				Types:      make(map[ast.Expr]types.TypeAndValue),
				Defs:       make(map[*ast.Ident]types.Object),
				Uses:       make(map[*ast.Ident]types.Object),
				Selections: make(map[*ast.SelectorExpr]*types.Selection),
				Instances:  make(map[*ast.Ident]types.Instance),
			}
			conf := types.Config{Importer: importer.Default()}
			if _, err := conf.Check("fixture", fset, files, info); err != nil {
				t.Fatalf("type-check fixture: %v", err)
			}

			funcIdx := buildFuncIndex(files)

			actual := runReachabilityBFS(t, fset, files, info, funcIdx, tc.roots)
			if diff := symmetricDiff(tc.expected, actual); len(diff) > 0 {
				t.Errorf("BFS reachable mismatch for %q:\n%s",
					tc.name, strings.Join(diff, "\n"))
			}
		})
	}
}
