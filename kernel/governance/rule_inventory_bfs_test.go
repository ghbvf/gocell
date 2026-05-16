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
// Fixtures use only built-in Go types (no external imports); the
// importer.Default() type-checker config is sufficient for that scope.
// Adding fixtures that import third-party packages would require a
// module-aware loader (packages.Load) similar to the production
// loadGovernancePackageWithTypes path.
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
			// BFS reaches helper(v) via the free-function call edge.
			// Inside helper (recvName=""), enqueueMethodValue early-returns
			// on the empty recvName, but handleCall's selector branch
			// resolves v.newResult via ResolveCallee + signature gate,
			// captures the rule ID via that path — not via the
			// enqueueMethodValue path.
			source: `package fixture
type RuleCode string
type ValidationResult struct{ Code RuleCode }
type locator struct{}
func (l *locator) newResult(s RuleCode) ValidationResult { return ValidationResult{} }
type Validator struct{ locator }
func (v *Validator) rules()                              { helper(v) }
func helper(v *Validator)                                { v.newResult("FIX-A-01") }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"FIX-A-01"},
		},
		{
			name: "non_validationresult_composite_literal_skipped",
			source: `package fixture
type RuleCode string
type ValidationResult struct{ Code string }
type locator struct{}
type Validator struct{ locator }
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
type RuleCode string
type ValidationResult struct{ Code string }
type locator struct{}
type Validator struct{ locator }
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
type RuleCode string
type ValidationResult struct{ Code RuleCode }
type locator struct{}
func (l *locator) newResult(s RuleCode) ValidationResult { return ValidationResult{} }
type Validator struct{ locator }
func (v *Validator) rules()                              { v.live() }
func (v *Validator) live()                               { v.newResult("LIVE-04") }
func (v *Validator) dead()                               { v.newResult("DEAD-99") }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"LIVE-04"},
		},
		{
			name: "const_ident_emission_resolved_via_const_map",
			source: `package fixture
type RuleCode string
type ValidationResult struct{ Code RuleCode }
type locator struct{}
func (l *locator) newResult(s RuleCode) ValidationResult { return ValidationResult{} }
type Validator struct{ locator }
const ruleX RuleCode = "X-CONST-05"
func (v *Validator) rules()                              { v.do() }
func (v *Validator) do()                                 { v.newResult(ruleX) }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: []string{"X-CONST-05"},
		},
		{
			name: "signature_mismatched_same_named_method_ignored_RED",
			// Method literally named newResult with RuleCode arg 0 but
			// missing the ValidationResult return type — handleCall's
			// result-arity gate (Results().Len() != 1) rejects before
			// the owner gate runs.
			source: `package fixture
type RuleCode string
type ValidationResult struct{ Code RuleCode }
type locator struct{}
func (l *locator) newResult(s RuleCode) {}
type Validator struct{ locator }
func (v *Validator) rules() { v.newResult("RED-06") }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil,
		},
		{
			name: "variadic_format_emitter_ignored_RED",
			// A method with the canonical (string, ...) → ValidationResult
			// shape but variadic must NOT be treated as an emitter:
			// x.Args[0] would be the format template, not a rule ID.
			// Variadic gate fires first regardless of receiver identity.
			source: `package fixture
type RuleCode string
type ValidationResult struct{ Code string }
type locator struct{}
func (l *locator) newResultf(fmtStr string, args ...interface{}) ValidationResult { return ValidationResult{} }
type Validator struct{ locator }
func (v *Validator) rules() { v.newResultf("rule %s applied", "FOO-BAR") }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil,
		},
		{
			name: "interface_dispatch_emitter_ignored_RED",
			// Interface-dispatched calls return nil from typeutil.StaticCallee,
			// so handleCall early-returns via ResolveCallee's ok=false branch
			// before any signature predicates run. This fixture pins that
			// safety boundary: even if an interface method's signature shape
			// matches an emitter, dynamic dispatch is not statically
			// resolvable and therefore not picked up.
			//
			// Cross-package emitter rejection: post-R2-P1 the owner gate
			// is types.Identical(recvNamed, locatorType) using the
			// loaded package's locator type, so a same-named "locator"
			// in a foreign package would produce a distinct *types.Named
			// and Identical returns false — no cross-package leak path.
			// Validated structurally; no synthetic test required.
			source: `package fixture
type RuleCode string
type ValidationResult struct{ Code string }
type locator struct{}
type Emitter interface{ newResult(s string) ValidationResult }
type Validator struct{ e Emitter }
func (v *Validator) rules() { v.e.newResult("IFACE-07") }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil,
		},
		{
			name: "embedded_locator_outer_receiver_ignored_RED",
			// Reviewer F-1 (PR #521) RED: Go method-set promotion via
			// embedding inherits *locator's method set into the outer
			// Validator's method set. An earlier marker-iface-based
			// owner gate (types.Implements) returned true for *Validator
			// and accepted any emitter-shape method added there. This
			// fixture pins the actual R2-P1 invariant ("only *locator
			// is an emitter holder"): under the types.Identical owner
			// gate, recvNamed=Validator is not identical to locator,
			// so fakeEmit is rejected and "EMBED-PROMO-09" stays out
			// of reachable.
			source: `package fixture
type RuleCode string
type ValidationResult struct{ Code RuleCode }
type locator struct{}
func (l *locator) newResult(s RuleCode) ValidationResult { return ValidationResult{} }
type Validator struct{ locator }
func (v *Validator) rules()                               { _ = v.fakeEmit("EMBED-PROMO-09") }
func (v *Validator) fakeEmit(s RuleCode) ValidationResult { return ValidationResult{} }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil,
		},
		{
			name: "unrelated_receiver_with_emitter_shape_ignored_RED",
			// R2-P1 owner-allowlist RED proof: perfect emitter shape
			// (RuleCode arg 0 + ValidationResult return + same package)
			// on an unrelated receiver Helper that has no relation to
			// locator. Pre-R2-P1 (signature + same-pkg owner only)
			// captured "UNRELATED-08" into reachable; post-R2-P1
			// types.Identical(Helper, locator) returns false → rejected.
			// Complements embedded_locator_outer_receiver_ignored_RED
			// (which covers the embedding-promotion path) by covering
			// the "completely unrelated type" path.
			source: `package fixture
type RuleCode string
type ValidationResult struct{ Code RuleCode }
type locator struct{}
type Validator struct{ locator }
type Helper struct{}
func (v *Validator) rules()                          { v.delegate() }
func (v *Validator) delegate()                       { _ = Helper{}.emit("UNRELATED-08") }
func (h Helper) emit(s RuleCode) ValidationResult    { return ValidationResult{} }
`,
			roots:    []funcKey{{recv: "Validator", name: "rules"}},
			expected: nil,
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
			pkg, err := conf.Check("fixture", fset, files, info)
			if err != nil {
				t.Fatalf("type-check fixture: %v", err)
			}

			funcIdx := buildFuncIndex(files)
			gate := resolveEmitterGate(t, pkg.Scope())

			actual := runReachabilityBFS(t, fset, files, info, funcIdx, tc.roots, gate)
			if diff := symmetricDiff(tc.expected, actual); len(diff) > 0 {
				t.Errorf("BFS reachable mismatch for %q:\n%s",
					tc.name, strings.Join(diff, "\n"))
			}
		})
	}
}
