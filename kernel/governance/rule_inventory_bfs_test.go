package governance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestBFSReachabilityFixtures locks in the BFS edge-resolution behavior
// that the goldenRuleIDs comparison alone cannot prove. Each fixture
// synthesizes a tiny package and runs runReachabilityBFS — the same
// routine TestRuleReachabilityFromRegistrationRoots uses — then asserts
// the expected reachable set.
//
// The fixtures pin the post-PR-B tightenings:
//   - emission detection works inside free functions that take a
//     *Validator parameter (BFS sees `v.newResult(...)` even when the
//     enclosing receiver name is "");
//   - composite literals nested in non-ValidationResult slices do not
//     contribute foreign Code field values to reachable;
//   - BFS scope is bounded by roots — orphan methods are not visited;
//   - const-ident emission resolves through scanPackageConstStrings.
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
			// Pre-relaxation this would be missed because the CallExpr
			// emission branch required recvIdent.Name == recvName.
			source: `package fixture
type Validator struct{}
func (v *Validator) rules()             { helper(v) }
func helper(v *Validator)               { v.newResult("FIX-A-01") }
func (v *Validator) newResult(s string) {}
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"FIX-A-01"},
		},
		{
			name: "non_validationresult_composite_literal_skipped",
			// A foreign struct with a Code field nested inside []Other{{}}
			// must not contribute to reachable. Pre-tightening, the
			// inner nil-Type literal would be accepted by the permissive
			// fallback and "FOREIGN-02" would slip into reachable.
			source: `package fixture
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
			// Sanity check on the parent-context pre-pass: a nil-Type
			// inner literal whose outer is []ValidationResult IS still
			// recognized, so DOC-NAME-style emissions through helper
			// constructors keep working.
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
			// dead() is not in any registration root and not transitively
			// reached from rules(). Its emission must not appear.
			source: `package fixture
type Validator struct{}
func (v *Validator) rules()             { v.live() }
func (v *Validator) live()              { v.newResult("LIVE-04") }
func (v *Validator) dead()              { v.newResult("DEAD-99") }
func (v *Validator) newResult(s string) {}
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"LIVE-04"},
		},
		{
			name: "const_ident_emission_resolved_via_const_map",
			// Package-level const string is emitted as the rule code
			// argument; resolveIDArg looks it up in scanPackageConstStrings'
			// output. Mirrors how rules_misc_strict.go uses ruleFMT20..25.
			source: `package fixture
type Validator struct{}
const ruleX = "X-CONST-05"
func (v *Validator) rules()             { v.do() }
func (v *Validator) do()                { v.newResult(ruleX) }
func (v *Validator) newResult(s string) {}
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"X-CONST-05"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture.go", tc.source, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			files := []*ast.File{f}
			funcIdx := buildFuncIndex(files)

			actual := runReachabilityBFS(t, fset, files, funcIdx, tc.roots)
			if diff := symmetricDiff(tc.expected, actual); len(diff) > 0 {
				t.Errorf("BFS reachable mismatch for %q:\n%s",
					tc.name, strings.Join(diff, "\n"))
			}
		})
	}
}
