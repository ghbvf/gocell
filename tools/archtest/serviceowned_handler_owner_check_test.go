// INVARIANT: SERVICEOWNED-HANDLER-OWNER-CHECK-01
//
// Package archtest enforces SERVICEOWNED-HANDLER-OWNER-CHECK-01 (Hard):
// every contract.yaml HTTP endpoint with auth.serviceOwned=true must
// perform its service-layer ownership check exclusively through the
// runtime/auth.CheckOwner typed funnel, never via inline errcode.New
// with KindNotFound.
//
// AI-robust evaluation:
//
//   - **B1 downstream handler-adapter-bound reachability lock** — Hard.
//     Each serviceOwned slice's service.go must invoke
//     runtime/auth.CheckOwner transitively from a FuncDecl whose name
//     appears in the handler-adapter-resolved entry set. The entry set
//     is derived by scanning sibling files (handler.go etc) for
//     CallExpr where the receiver static type is the local *Service
//     struct — those method names are the contract's *real* execution
//     entries. BFS closure over intra-file function/method calls
//     determines reachability. Rejects:
//
//   - dead code at file scope (`var _ = auth.CheckOwner[any]{...}`)
//
//   - unexported helpers no entry transitively calls
//
//   - unexported/exported methods NOT invoked by the adapter (B1
//     binds to the contract's actual path, not "any Service method")
//     Package-path resolution via ResolvePackageRef prevents
//     import-alias / dot-import / same-named CheckOwner in another
//     package from evading. The conservative-entry-set fallback was
//     dropped in round-4; SSA-based callgraph upgrade (precise
//     interprocedural reachability beyond intra-file) tracked at
//     gh issue #1199 (ARCHTEST-SERVICEOWNED-SSA-CALLGRAPH-UPGRADE).
//
//   - **B2 funnel body lock** — Hard, four sub-conditions: (a) exactly
//     one errcode.New call resolving to KindNotFound with zero
//     other-Kind errcode.New calls (count uniqueness); (b) every non-nil
//     return value in the body is the canonical errcode.New(KindNotFound,
//     ...) call (return-form uniqueness); (c) the guard IF.Cond is
//     exactly a call to the same-package ownershipMismatch helper
//     (typed-function-choice condition lock); (d) the ownershipMismatch
//     helper body is exactly `callerID == "" || ownerID(resource) !=
//     callerID` with operand identities bound to its FuncDecl params
//     (B2b body shape lock). Together these prove all non-nil exits go
//     through the IDOR-safe 404 collapse form AND the mismatch
//     semantics cannot be operator-flipped or rewritten without the
//     archtest noticing.
//
//   - **B3 upstream zero-tolerance ban** — Hard. service.go files in
//     serviceOwned slices must contain zero
//     errcode.New(KindNotFound, ...) AND zero errcode.Wrap(KindNotFound,
//     ...) callsites. Detection is a single-assertion count == 0 over
//     both Kind-bearing constructors with no AST form matching, no
//     carve-out, and no cross-function helper escape: wrapping the raw
//     errcode.New/Wrap in a helper still counts toward the production
//     scope. Lookup-failure paths must collapse through the funnel via
//     nil-safe accessors (sessionlogout/service.go canonical form),
//     preserving the IDOR-safe 404 collapse semantics. The funnel's
//     own body lives in runtime/, not cells/, and is therefore out of
//     B3 scope by construction.
//
//     Typed const alias `const k = errcode.KindNotFound; errcode.New(k,
//     ...)` IS caught: isKindNotFoundArg first compares the const-folded
//     value of arg (via types.Info.Types[arg].Value) against the constant
//     value of errcode.KindNotFound looked up from pass.Pkg's imports.
//     This branch fires before ResolvePackageRef, covering the const
//     alias path that bare package-ref resolution misses (red fixture
//     red_b3_const_alias_kindnotfound demonstrates).
//
//     Known SSA-pending blindspot (#1199): runtime-var alias `local :=
//     errcode.KindNotFound; errcode.New(local, ...)` is NOT caught —
//     local is a *types.Var (not *types.Const), so Types[local].Value
//     is nil and the const-folding branch skips it; ResolvePackageRef
//     also returns false (local is not a package-level symbol). SSA
//     dataflow is required to follow runtime variable values; tracked
//     at gh #1199 alongside the B1 callgraph upgrade.
//
// Hard 範本目录 mapping: this funnel realizes "typed function choice" +
// "typed marker funnel for unbounded ops" — `auth.CheckOwner` is the
// only API name carrying the IDOR-safe semantics, and any other AST form
// constructing KindNotFound in serviceOwned service.go is rejected.
//
// Blindspot inventory (tools: metadata.NewParser + Run(t, Typed(...)) +
// archtest.ResolvePackageRef + EachInSubtree[ast.CallExpr]):
//
//   - Cross-package re-export: a hypothetical `cells/foo.CheckOwner`
//     that wraps `runtime/auth.CheckOwner` would pass B1's package
//     identity check by virtue of the wrapper itself calling the funnel.
//     This is acceptable — the wrapper, being a thin pass-through, still
//     routes all ownership decisions through the canonical funnel body.
//     A divergent wrapper (returning a different Kind) cannot exist
//     without itself calling errcode.New(KindNotFound, ...) somewhere,
//     which B3 catches in any cells/* service.go scope.
//
//   - Service file location: the rule scans
//     cells/<cellDir>/slices/<sliceDir>/service.go only. If a slice
//     places its ownership logic in a different file, B1 and B3 both
//     miss it. Mitigation: canonical GoCell slices use service.go;
//     SERVICE-02..05 sub-rules of OUTBOX-SERVICE-01 enforce the
//     convention.
//
// Self-check: TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture
// loads nine testdata packages with full types.Info via Run(t, Typed(...)),
// sharing the same predicate closures as the production scans. Each
// predicate is exercised on both a green path (silent) and one or more red
// paths (firing), and cross-predicate silence is asserted on red fixtures
// targeting other predicates (B1 RED fixtures must be silent on B3, etc.)
// to keep B1/B2/B3 independently distinguishable. B1 reachability is
// validated by the red_b1_dead_callsite and red_b1_unreachable_helper
// fixtures; B2 return-form lock is validated by the red_b2_alternative_return
// fixture.
//
// CI gating: this archtest is **nightly-only** at present — it runs via
// archtest-nightly.yml (24-shard, ADR 202605120000 §Amendment 2026-05-28)
// and locally via `make verify` / `bash hack/verify-archtest.sh`, but is
// NOT in PR-time
// `hack/verify-archtest-invariants.sh`. The PR-time set is frozen by ADR
// `docs/architecture/202605120000` §Amendment 2026-05-23 §D8 to four
// categories (clock / duration / testtime / panic) plus the
// fence-token-funnel addition; adding SERVICEOWNED requires a §D8
// amendment evaluating CPU/RSS budget against the existing whole-tree
// scans. Until then, IDOR-regression catch latency is at most one nightly
// cycle (developers running `make verify` locally also see violations
// pre-PR).
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

const (
	ruleServiceOwnedOwnerCheck01 = "SERVICEOWNED-HANDLER-OWNER-CHECK-01"
)

// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01 runs all three Hard predicates
// (B1+B2+B3) against production code.
func TestSERVICEOWNED_HANDLER_OWNER_CHECK_01(t *testing.T) {
	t.Parallel()
	Report(t, ruleServiceOwnedOwnerCheck01, CheckServiceownedHandlerOwnerCheck01(t, ConfigForExternalCell{}))
}

// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture verifies all three
// predicates fire correctly on RED fixtures and stay silent on GREEN.
//
// Fixture naming convention: `green*/` = expected B1/B3/B2 silent;
// `red_*/` = expected to fire the named predicate. The matrix asserts both
// firing (RED) and silence (GREEN, plus cross-predicate silence on RED
// fixtures targeting other predicates) so B1/B2/B3 are independently
// distinguishable.
//
// Fixture layout:
//
//   - green/service.go: uses auth.CheckOwner, no raw KindNotFound → B1+B3 silent
//   - green_funnel_body/owner_guard.go: canonical funnel body → B2 silent
//   - red_missing_check_owner/service.go: no auth.CheckOwner → B1 fires; B3 silent
//   - red_raw_kindnotfound_in_service/service.go: has CheckOwner AND raw
//     errcode.New(KindNotFound,...) → B3 fires; B1 silent
//   - red_b1_dead_callsite/service.go: auth.CheckOwner at file scope
//     inside `var _ = func() { ... }`, not reachable from any exported
//     FuncDecl → B1 fires; B3 silent
//   - red_b1_unreachable_helper/service.go: auth.CheckOwner inside an
//     unexported helper that no exported entry method calls → B1 fires;
//     B3 silent
//   - red_funnel_body_wrong_kind/owner_guard.go: CheckOwner returns
//     KindPermissionDenied → B2 fires
//   - red_funnel_body_extra_new/owner_guard.go: CheckOwner has two errcode.New
//     calls → B2 fires
//   - red_b2_alternative_return/owner_guard.go: CheckOwner has canonical
//     KindNotFound exit AND a bypass `return errors.New(...)` path → B2
//     fires (return-form lock)
func TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture(t *testing.T) {
	t.Parallel()

	fixtureBase := "tools/archtest/testdata/serviceowned_handler_owner_check"

	type predicate int
	const (
		predB1 predicate = iota
		predB2
		predB3
	)

	cases := []struct {
		subdir         string
		pred           predicate
		wantViolations bool
	}{
		// B1 / B3 fixtures (service.go scope) — green path
		{subdir: "green", pred: predB1, wantViolations: false},
		{subdir: "green", pred: predB3, wantViolations: false},
		// B1 RED + B3 silent cross-check
		{subdir: "red_missing_check_owner", pred: predB1, wantViolations: true},
		{subdir: "red_missing_check_owner", pred: predB3, wantViolations: false},
		// B3 RED + B1 silent cross-check
		{subdir: "red_raw_kindnotfound_in_service", pred: predB1, wantViolations: false},
		{subdir: "red_raw_kindnotfound_in_service", pred: predB3, wantViolations: true},
		// B3 RED via errcode.Wrap (not New) + B1 silent cross-check
		{subdir: "red_b3_wrap_kindnotfound", pred: predB1, wantViolations: false},
		{subdir: "red_b3_wrap_kindnotfound", pred: predB3, wantViolations: true},
		// B3 RED via typed const alias (const k = errcode.KindNotFound)
		// — caught by const-folding branch, NOT package-ref resolution
		{subdir: "red_b3_const_alias_kindnotfound", pred: predB1, wantViolations: false},
		{subdir: "red_b3_const_alias_kindnotfound", pred: predB3, wantViolations: true},
		// B1 reachability RED fixtures (file-scope dead code + unreachable helper)
		{subdir: "red_b1_dead_callsite", pred: predB1, wantViolations: true},
		{subdir: "red_b1_dead_callsite", pred: predB3, wantViolations: false},
		{subdir: "red_b1_unreachable_helper", pred: predB1, wantViolations: true},
		{subdir: "red_b1_unreachable_helper", pred: predB3, wantViolations: false},
		// B1 unmapped-entry RED: CheckOwner exists in Logout but adapter
		// invokes SomeOtherOp — entry-bound BFS catches the bypass.
		{subdir: "red_b1_unmapped_entry", pred: predB1, wantViolations: true},
		{subdir: "red_b1_unmapped_entry", pred: predB3, wantViolations: false},
		// B2 fixtures (owner_guard.go scope) — green + red
		{subdir: "green_funnel_body", pred: predB2, wantViolations: false},
		{subdir: "red_funnel_body_wrong_kind", pred: predB2, wantViolations: true},
		{subdir: "red_funnel_body_extra_new", pred: predB2, wantViolations: true},
		// B2 return-form RED: canonical exit PLUS a bypass return path
		{subdir: "red_b2_alternative_return", pred: predB2, wantViolations: true},
		// B2 condition lock RED: helper present but unused (inline predicate)
		{subdir: "red_b2_helper_unused", pred: predB2, wantViolations: true},
		// B2b helper body lock RED: helper present + used, but body drifted
		{subdir: "red_b2_helper_body_drift", pred: predB2, wantViolations: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("%s_pred%d", tc.subdir, tc.pred), func(t *testing.T) {
			t.Parallel()

			pattern := "./" + fixtureBase + "/" + tc.subdir
			diags := Run(t, Typed(
				TypedOpts{Tests: false, Tags: nil},
				[]string{pattern},
			),

				func(pass *Pass) []Diagnostic {
					if !pass.Typed() {
						return nil
					}
					var d []Diagnostic

					if tc.pred == predB1 || tc.pred == predB3 {
						var serviceFile *ast.File
						var siblings []*ast.File
						for _, f := range pass.Files {
							rel := pass.Rel(f)
							if strings.HasSuffix(rel, "_test.go") {
								continue
							}
							if strings.HasSuffix(rel, "/service.go") {
								serviceFile = f
							} else {
								siblings = append(siblings, f)
							}
						}
						if serviceFile == nil {
							return nil
						}
						entries := resolveHandlerAdapterEntries(pass, siblings)
						rel := pass.Rel(serviceFile)
						switch tc.pred {
						case predB1:
							d = append(d, checkServiceFileCalleeLock(pass, serviceFile, rel, "fixture-contract", entries)...)
						case predB3:
							d = append(d, checkServiceFileZeroToleranceBan(pass, serviceFile, rel, "fixture-contract")...)
						}
					} else if tc.pred == predB2 {
						for _, file := range pass.Files {
							rel := pass.Rel(file)
							d = append(d, checkFunnelBody(pass, file, rel)...)
						}
					}
					return d
				})

			if tc.wantViolations && len(diags) == 0 {
				t.Errorf("%s: fixture %q pred=%d expected ≥1 diagnostic got 0",
					ruleServiceOwnedOwnerCheck01, tc.subdir, tc.pred)
			}
			if !tc.wantViolations && len(diags) > 0 {
				t.Errorf("%s: fixture %q pred=%d expected 0 diagnostics got %d:",
					ruleServiceOwnedOwnerCheck01, tc.subdir, tc.pred, len(diags))
				for _, d := range diags {
					t.Errorf("  %s:%d: %s", d.Rel, d.Line, d.Message)
				}
			}
		})
	}
}
