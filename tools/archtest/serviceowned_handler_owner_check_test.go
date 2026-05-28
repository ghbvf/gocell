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
// Hard 范本目录 mapping: this funnel realizes "typed function choice" +
// "typed marker funnel for unbounded ops" — `auth.CheckOwner` is the
// only API name carrying the IDOR-safe semantics, and any other AST form
// constructing KindNotFound in serviceOwned service.go is rejected.
//
// Blindspot inventory (tools: metadata.NewParser + archtest.RunTyped +
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
// loads nine testdata packages with full types.Info via archtest.RunTyped,
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
// archtest-nightly.yml (16-shard) and locally via `make verify` /
// `bash hack/verify-archtest.sh`, but is NOT in PR-time
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
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

const (
	ruleServiceOwnedOwnerCheck01 = "SERVICEOWNED-HANDLER-OWNER-CHECK-01"
	// serviceOwnedErrcodePkg is the canonical import path of the errcode package.
	serviceOwnedErrcodePkg = "github.com/ghbvf/gocell/pkg/errcode"
	// serviceOwnedKindNotFoundSym is the symbol name within errcodePkg that
	// constitutes the IDOR-safe 404-collapse error kind.
	serviceOwnedKindNotFoundSym = "KindNotFound"
	// serviceOwnedAuthPkg is the canonical import path of the runtime/auth
	// package where the CheckOwner funnel lives.
	serviceOwnedAuthPkg = "github.com/ghbvf/gocell/runtime/auth"
	// serviceOwnedCheckOwnerSym is the function name that constitutes the
	// sole sanctioned ownership-check funnel.
	serviceOwnedCheckOwnerSym = "CheckOwner"
	// serviceOwnedMismatchHelperSym is the unexported same-package helper
	// that encapsulates the "what counts as owner mismatch" predicate.
	// Factoring this out of CheckOwner's IF condition makes the B2 condition
	// lock a typed-function-choice form check (single callsite shape) instead
	// of a brittle AST-tree match.
	serviceOwnedMismatchHelperSym = "ownershipMismatch"
	// serviceOwnedOwnerGuardFile is the production funnel implementation
	// file. B2 asserts exactly this file contains the canonical funnel.
	serviceOwnedOwnerGuardFile = "runtime/auth/owner_guard.go"
)

// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01 runs all three Hard predicates
// against production code.
//
// B1: each serviceOwned slice's service.go contains ≥1 auth.CheckOwner call
// B2: runtime/auth/owner_guard.go::CheckOwner body has exactly 1
//
//	errcode.New(KindNotFound, ...) call and no other-Kind errcode.New
//
// B3: each serviceOwned slice's service.go contains 0
//
//	errcode.New(KindNotFound, ...) callsites
func TestSERVICEOWNED_HANDLER_OWNER_CHECK_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("%s: metadata.NewParser: %v", ruleServiceOwnedOwnerCheck01, err)
	}

	serviceOwnedContracts := collectServiceOwnedContracts(project)

	// Build target set (service.go paths for each serviceOwned slice).
	type target struct {
		rel        string
		abs        string
		contractID string
		sliceID    string
	}
	var targets []target
	var missingFileDiags []Diagnostic

	for contractID, servingSlices := range serviceOwnedContracts {
		for _, sl := range servingSlices {
			rel := filepath.ToSlash(
				filepath.Join("cells", sl.CellDir, "slices", sl.Dir, "service.go"),
			)
			absPath := filepath.Join(root, rel)
			if _, statErr := os.Stat(absPath); os.IsNotExist(statErr) {
				missingFileDiags = append(missingFileDiags, Diagnostic{
					Rel: rel, Line: 0,
					Message: fmt.Sprintf(
						"contract %q (auth.serviceOwned=true) serves slice %q but %s not found",
						contractID, sl.ID, rel,
					),
				})
				continue
			}
			targets = append(targets, target{
				rel: rel, abs: absPath,
				contractID: contractID, sliceID: sl.ID,
			})
		}
	}
	Report(t, ruleServiceOwnedOwnerCheck01, missingFileDiags)

	// Load production sources with type info: cells/ for B1/B3, runtime/auth/ for B2.
	seenRels := map[string]bool{}
	diags := RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		[]string{"./cells/...", "./runtime/auth/..."},
		func(pass *Pass) []Diagnostic {
			if !pass.Typed() {
				return nil
			}
			var d []Diagnostic
			// B2 funnel body on owner_guard.go (independent of targets)
			for _, file := range pass.Files {
				rel := pass.Rel(file)
				seenRels[rel] = true
				if rel == serviceOwnedOwnerGuardFile {
					d = append(d, checkFunnelBody(pass, file, rel)...)
				}
			}
			// B1+B3 per serviceOwned target: collect service.go + sibling
			// adapter files (handler.go etc), resolve adapter-bound entry
			// methods, then run reachability + ban predicates.
			for i := range targets {
				target := &targets[i]
				sliceDir := path.Dir(target.rel)
				var serviceFile *ast.File
				var siblings []*ast.File
				for _, f := range pass.Files {
					rel := pass.Rel(f)
					if path.Dir(rel) != sliceDir {
						continue
					}
					if strings.HasSuffix(rel, "_test.go") {
						continue
					}
					if rel == target.rel {
						serviceFile = f
					} else {
						siblings = append(siblings, f)
					}
				}
				if serviceFile == nil {
					continue
				}
				entries := resolveHandlerAdapterEntries(pass, siblings)
				d = append(d, checkServiceFileCalleeLock(pass, serviceFile, target.rel, target.contractID, entries)...)
				d = append(d, checkServiceFileZeroToleranceBan(pass, serviceFile, target.rel, target.contractID)...)
			}
			return d
		})

	// Cross-check: any target whose service.go was never loaded (e.g. build-tag
	// exclusion) deserves an explicit diagnostic so B1/B3 are not silently
	// skipped.
	for i := range targets {
		if !seenRels[targets[i].rel] {
			diags = append(diags, Diagnostic{
				Rel: targets[i].rel, Line: 0,
				Message: fmt.Sprintf(
					"contract %q: %s not loaded by typeseval "+
						"(build tag excluded?); B1/B3 check skipped",
					targets[i].contractID, targets[i].rel,
				),
			})
		}
	}
	if !seenRels[serviceOwnedOwnerGuardFile] {
		diags = append(diags, Diagnostic{
			Rel: serviceOwnedOwnerGuardFile, Line: 0,
			Message: fmt.Sprintf(
				"%s not loaded by typeseval; B2 funnel body check skipped",
				serviceOwnedOwnerGuardFile,
			),
		})
	}

	Report(t, ruleServiceOwnedOwnerCheck01, diags)
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
			diags := RunTyped(t,
				TypedOpts{Tests: false, Tags: nil},
				[]string{pattern},
				func(pass *Pass) []Diagnostic {
					if !pass.Typed() {
						return nil
					}
					var d []Diagnostic
					// For B1/B3 fixtures, identify service.go + adapter siblings.
					// For B2 fixtures, the package only contains owner_guard.go.
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

// collectServiceOwnedContracts returns a map from contract ID to the list of
// SliceMeta entries that serve that contract (contractUsages role="serve").
func collectServiceOwnedContracts(project *metadata.ProjectMeta) map[string][]*metadata.SliceMeta {
	result := map[string][]*metadata.SliceMeta{}
	for contractID, contract := range project.Contracts {
		if contract.Kind != "http" || contract.Endpoints.HTTP == nil {
			continue
		}
		if !contract.Endpoints.HTTP.Auth.ServiceOwned {
			continue
		}
		for _, sl := range project.Slices {
			for _, usage := range sl.ContractUsages {
				if usage.Contract == contractID && usage.Role == "serve" {
					result[contractID] = append(result[contractID], sl)
					break
				}
			}
		}
	}
	return result
}

// checkServiceFileCalleeLock (B1) reports a diagnostic if no exported
// FuncDecl in file that is in the handler-adapter-resolved entry set can
// transitively reach a call to runtime/auth.CheckOwner via intra-file
// function/method invocations.
//
// entries is the set of Service method names invoked by sibling adapter
// files (handler.go etc.) for the serviceOwned contract. An empty entries
// set means no adapter calls were discovered, which itself fires a
// diagnostic (the canonical slice structure requires an adapter wiring).
func checkServiceFileCalleeLock(pass *Pass, file *ast.File, rel, contractID string, entries map[string]bool) []Diagnostic {
	if len(entries) == 0 {
		return []Diagnostic{{
			Rel: rel, Line: 0,
			Message: fmt.Sprintf(
				"contract %q (auth.serviceOwned=true) serving slice in %s "+
					"has no handler.go adapter file calling a Service "+
					"method — B1 reachability cannot be validated without "+
					"an adapter binding to identify the contract-mapped "+
					"entry method. Canonical structure: handler.go's "+
					"Adapter.<HTTPMethod>(...) calls a.S.<ServiceMethod>(...) "+
					"which (transitively) calls auth.CheckOwner.",
				contractID, rel,
			),
		}}
	}
	if fileCallsAuthCheckOwnerFromEntries(pass.TypesInfo, file, entries) {
		return nil
	}
	return []Diagnostic{{
		Rel: rel, Line: 0,
		Message: fmt.Sprintf(
			"contract %q (auth.serviceOwned=true) serving slice in %s "+
				"is missing an auth.CheckOwner call reachable from the "+
				"handler-adapter-resolved entry set (%v). Service-layer "+
				"ownership checks must go through the CheckOwner funnel "+
				"(see runtime/auth/owner_guard.go). Canonical form: "+
				"cells/accesscore/slices/sessionlogout/service.go "+
				"(Service.Logout calls Service.revokeAndPublish which "+
				"calls auth.CheckOwner) — dead code at file scope and "+
				"unreachable helpers do not satisfy reachability. "+
				"B3 separately enforces zero raw "+
				"errcode.New/Wrap(errcode.KindNotFound, ...) in this file.",
			contractID, rel, sortedKeys(entries),
		),
	}}
}

// resolveHandlerAdapterEntries scans sibling files (typically handler.go)
// for CallExpr where the receiver's static type is the local "Service"
// struct, and returns the set of method names invoked. These are the
// contract-mapped service entries B1 BFS will seed from.
//
// "Service" type is identified by name convention (the canonical GoCell
// slice convention; OUTBOX-SERVICE-01 enforces). If the package does not
// declare a "Service" type, returns an empty set — checkServiceFileCalleeLock
// will then fire the "no adapter" diagnostic.
func resolveHandlerAdapterEntries(pass *Pass, siblings []*ast.File) map[string]bool {
	entries := map[string]bool{}
	if pass.Pkg == nil {
		return entries
	}
	serviceObj := pass.Pkg.Scope().Lookup("Service")
	if serviceObj == nil {
		return entries
	}
	serviceType := serviceObj.Type()
	ptrToService := types.NewPointer(serviceType)
	for _, file := range siblings {
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			recvType := pass.TypesInfo.TypeOf(sel.X)
			if recvType == nil {
				return
			}
			if types.Identical(recvType, serviceType) || types.Identical(recvType, ptrToService) {
				if sel.Sel != nil {
					entries[sel.Sel.Name] = true
				}
			}
		})
	}
	return entries
}

// sortedKeys returns map keys in deterministic order for diagnostic
// formatting (avoids non-determinism in error messages).
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// simple insertion sort — small N
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// checkServiceFileZeroToleranceBan (B3) reports a diagnostic for every
// errcode.New(errcode.KindNotFound, ...) call in file. Zero is the only
// passing count.
func checkServiceFileZeroToleranceBan(pass *Pass, file *ast.File, rel, contractID string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isErrCodeNewOrWrapCall(pass.TypesInfo, call) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		if !isKindNotFoundArg(pass, call.Args[0]) {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: pass.Fset.Position(call.Pos()).Line,
			Message: fmt.Sprintf(
				"contract %q (auth.serviceOwned=true): %s contains a raw "+
					"errcode.New / errcode.Wrap with errcode.KindNotFound. "+
					"All KindNotFound returns in serviceOwned service.go "+
					"must come from the runtime/auth.CheckOwner funnel "+
					"(zero-tolerance ban — IDOR collapse depends on a "+
					"single sanctioned funnel body). Fix: delete this "+
					"call and let the nil-safe accessor (returns \"\" on "+
					"nil/not-found resource) collapse lookup-failure into "+
					"the funnel. See testdata fixtures green/ (correct form) "+
					"and red_raw_kindnotfound_in_service/ + "+
					"red_b3_wrap_kindnotfound/ (anti-patterns).",
				contractID, rel,
			),
		})
	})
	return diags
}

// checkFunnelBody (B2) reports diagnostics on the runtime/auth/owner_guard.go
// CheckOwner function body if it deviates from the canonical form:
//
//   - exactly one errcode.New call
//   - that one call's first argument resolves to errcode.KindNotFound
//   - no other errcode.New calls with any other Kind
//
// Called with the file = production owner_guard.go OR a fixture
// {green,red}_funnel_body_*/owner_guard.go (which must declare a top-level
// CheckOwner func).
func checkFunnelBody(pass *Pass, file *ast.File, rel string) []Diagnostic {
	var fn *ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fn != nil {
			return // first match wins
		}
		if fd.Name == nil || fd.Name.Name != serviceOwnedCheckOwnerSym {
			return
		}
		if fd.Recv != nil {
			return // funnel is a free function, not a method
		}
		fn = fd
	})
	if fn == nil {
		return []Diagnostic{{
			Rel: rel, Line: 0,
			Message: fmt.Sprintf(
				"%s does not declare a top-level CheckOwner function — "+
					"funnel implementation moved or renamed.",
				rel,
			),
		}}
	}

	var (
		notFoundCalls int
		otherCalls    []Diagnostic
	)
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		if !isErrCodeNewCall(pass.TypesInfo, call) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		if isKindNotFoundArg(pass, call.Args[0]) {
			notFoundCalls++
			return
		}
		otherCalls = append(otherCalls, Diagnostic{
			Rel:  rel,
			Line: pass.Fset.Position(call.Pos()).Line,
			Message: fmt.Sprintf(
				"%s CheckOwner body contains errcode.New with a "+
					"non-KindNotFound first argument — funnel has "+
					"drifted from IDOR-safe 404 collapse.",
				rel,
			),
		})
	})

	var diags []Diagnostic
	diags = append(diags, otherCalls...)
	if notFoundCalls != 1 {
		diags = append(diags, Diagnostic{
			Rel: rel, Line: pass.Fset.Position(fn.Pos()).Line,
			Message: fmt.Sprintf(
				"%s CheckOwner body must contain exactly one "+
					"errcode.New(errcode.KindNotFound, ...) call; found %d. "+
					"Multiple KindNotFound calls obscure the sole sanctioned "+
					"funnel exit and weaken Hard-form uniqueness.",
				rel, notFoundCalls,
			),
		})
	}
	// Return-form lock: every non-nil return value in the funnel body must be
	// the canonical errcode.New(KindNotFound, ...) form. Count-based check
	// above does not catch `return otherErr` paths that bypass errcode.New;
	// this walker does.
	diags = append(diags, checkFunnelReturnForms(pass, fn, rel)...)
	// B2 condition lock: the guard IF (the one whose body returns the
	// canonical errcode.New(KindNotFound,...)) must have its condition be a
	// single call to the sibling ownershipMismatch helper. Anything else
	// (inline operator-flipped predicate, additional conjunctions) fails the
	// typed-function-choice form check.
	diags = append(diags, checkFunnelGuardCondition(pass, fn, file, rel)...)
	// B2b ownershipMismatch helper body lock: the helper itself must encode
	// the canonical mismatch predicate `callerID == "" || ownerID(resource)
	// != callerID` with operand identities bound to the helper's FuncDecl
	// parameters. Drift (operator inversion, removed empty-callerID guard)
	// is rejected here. Together with the condition lock above, this proves
	// the funnel exits on exactly the canonical mismatch semantics.
	diags = append(diags, checkOwnershipMismatchBody(pass, file, rel)...)
	return diags
}

// checkFunnelGuardCondition (B2 supplement) finds the IF statement in
// CheckOwner whose body returns the canonical errcode.New(KindNotFound,...)
// and asserts its condition expression is exactly a same-package call to
// ownershipMismatch. The call shape is the "typed function choice" Hard
// form: writing the mismatch predicate inline (bypassing the helper) makes
// the funnel's IF condition a different AST node, and this check fires.
//
// file parameter is currently unused but reserved for future helper-locality
// validation (e.g. ensuring ownershipMismatch is defined in the same file
// as CheckOwner, not just the same package).
func checkFunnelGuardCondition(pass *Pass, fn *ast.FuncDecl, file *ast.File, rel string) []Diagnostic {
	_ = file
	var diags []Diagnostic
	guardCount := 0
	EachInChildren[ast.IfStmt](fn.Body, func(ifStmt *ast.IfStmt) {
		if !ifBodyReturnsCanonicalNotFound(pass, ifStmt.Body) {
			return
		}
		guardCount++
		if !isOwnershipMismatchCall(pass, ifStmt.Cond) {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pass.Fset.Position(ifStmt.Cond.Pos()).Line,
				Message: fmt.Sprintf(
					"%s CheckOwner guard IF condition is not a call to the "+
						"same-package ownershipMismatch helper. The IDOR-collapse "+
						"predicate must be funneled through ownershipMismatch so "+
						"future evolution of the mismatch semantics lives in one "+
						"place; inline predicates are rejected (operator-flip "+
						"bugs caught at the helper level).",
					rel,
				),
			})
		}
	})
	if guardCount == 0 {
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: pass.Fset.Position(fn.Pos()).Line,
			Message: fmt.Sprintf(
				"%s CheckOwner has no IF statement whose body returns "+
					"errcode.New(errcode.KindNotFound, ...) — the canonical "+
					"guard structure is missing.",
				rel,
			),
		})
	}
	return diags
}

// ifBodyReturnsCanonicalNotFound reports whether the IF body contains a
// ReturnStmt with at least one errcode.New call whose first argument
// resolves to errcode.KindNotFound (constant folding or package-ref).
// Used to identify the funnel's guard IF for the condition-lock predicate.
func ifBodyReturnsCanonicalNotFound(pass *Pass, body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	// SCANNER-FRAMEWORK-USAGE-01 Path B + USAGE-02 compliance:
	//   - Outer: FindFirstInSubtree[ast.ReturnStmt] replaces "EachInSubtree +
	//     found sentinel" idiom (USAGE-02 forbids the sentinel form on
	//     EachInChildren; the typed FindFirst* funnels are mandated for
	//     find-first semantics on either depth).
	//   - Inner: FindFirstChild[ast.CallExpr] over ret's direct children
	//     replaces "for _, expr := range ret.Results { expr.(*ast.CallExpr) }"
	//     (Path B violation form).
	_, match := FindFirstInSubtree[ast.ReturnStmt](body, func(ret *ast.ReturnStmt) bool {
		_, hit := FindFirstChild[ast.CallExpr](ret, func(call *ast.CallExpr) bool {
			if !isErrCodeNewCall(pass.TypesInfo, call) {
				return false
			}
			if len(call.Args) == 0 {
				return false
			}
			return isKindNotFoundArg(pass, call.Args[0])
		})
		return hit
	})
	return match
}

// isOwnershipMismatchCall reports whether cond is a CallExpr whose callee
// resolves via go/types to the same-package ownershipMismatch helper.
// The helper must be in the same Go package as CheckOwner — cross-package
// "ownershipMismatch" symbols are not accepted (would break the single-
// source-of-truth invariant).
func isOwnershipMismatchCall(pass *Pass, cond ast.Expr) bool {
	call, ok := cond.(*ast.CallExpr)
	if !ok {
		return false
	}
	ref := unwrapCalleeForResolve(call.Fun)
	if ref == nil {
		return false
	}
	name := calleeIdentName(ref)
	if name != serviceOwnedMismatchHelperSym {
		return false
	}
	// Verify the resolved object is defined in the same file (package
	// scope). types.Info.Uses[ident] gives us the *types.Func; we then
	// confirm its package matches the file's package.
	var id *ast.Ident
	switch v := ref.(type) {
	case *ast.Ident:
		id = v
	case *ast.SelectorExpr:
		id = v.Sel
	default:
		return false
	}
	if id == nil {
		return false
	}
	obj := pass.TypesInfo.ObjectOf(id)
	if obj == nil {
		return false
	}
	if obj.Pkg() == nil {
		return false
	}
	// Pkg path must match the package of the file under inspection.
	return obj.Pkg().Path() == pass.Pkg.Path()
}

// checkOwnershipMismatchBody (B2b) locks the ownershipMismatch helper body
// to the canonical AST form:
//
//	return callerID == "" || ownerID(resource) != callerID
//
// with operand identities bound to the helper's FuncDecl parameter list
// (positional binding: param[0]=resource, param[1]=ownerID, param[2]=callerID).
//
// Failure modes detected:
//   - Helper missing → fires "helper not declared"
//   - Body is not a single ReturnStmt → fires "body shape drift"
//   - Returned expression is not a LOR BinaryExpr → fires
//   - Empty-callerID guard (EQL with "" literal) is missing/flipped → fires
//   - Mismatch comparison (NEQ between accessor(resource) and callerID) is
//     missing/flipped (e.g. `==` instead of `!=`, or operand swap) → fires
func checkOwnershipMismatchBody(pass *Pass, file *ast.File, rel string) []Diagnostic {
	var helper *ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name != nil && fd.Name.Name == serviceOwnedMismatchHelperSym {
			helper = fd
		}
	})
	if helper == nil {
		return []Diagnostic{{
			Rel: rel, Line: 0,
			Message: fmt.Sprintf(
				"%s missing %s helper — CheckOwner's IF condition lock "+
					"requires a same-package ownershipMismatch[T] function "+
					"encapsulating the mismatch predicate.",
				rel, serviceOwnedMismatchHelperSym,
			),
		}}
	}
	// Extract helper params: resource (param 0), ownerID (param 1), callerID (param 2)
	params := flattenParamIdents(helper.Type.Params)
	if len(params) != 3 {
		return []Diagnostic{{
			Rel: rel, Line: pass.Fset.Position(helper.Pos()).Line,
			Message: fmt.Sprintf(
				"%s ownershipMismatch must have exactly 3 parameters "+
					"(resource T, ownerID func(T) string, callerID string); "+
					"found %d.",
				rel, len(params),
			),
		}}
	}
	resourceP, ownerIDP, callerIDP := params[0], params[1], params[2]

	// Helper body must be exactly a single ReturnStmt returning the canonical
	// LOR(EQL(callerID, ""), NEQ(Call(ownerID, resource), callerID)) expression.
	if helper.Body == nil || len(helper.Body.List) != 1 {
		return []Diagnostic{{
			Rel: rel, Line: pass.Fset.Position(helper.Pos()).Line,
			Message: fmt.Sprintf(
				"%s ownershipMismatch body must be exactly one return "+
					"statement; got %d statements.",
				rel, len(helper.Body.List),
			),
		}}
	}
	ret, ok := helper.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return []Diagnostic{{
			Rel: rel, Line: pass.Fset.Position(helper.Body.List[0].Pos()).Line,
			Message: fmt.Sprintf(
				"%s ownershipMismatch body must be a single ReturnStmt with "+
					"one expression.", rel,
			),
		}}
	}
	if !isCanonicalMismatchExpr(ret.Results[0], resourceP, ownerIDP, callerIDP) {
		return []Diagnostic{{
			Rel: rel, Line: pass.Fset.Position(ret.Results[0].Pos()).Line,
			Message: fmt.Sprintf(
				"%s ownershipMismatch return expression is not the canonical "+
					"form `callerID == \"\" || ownerID(resource) != callerID`. "+
					"Operator inversion or operand reordering would silently "+
					"flip IDOR semantics; B2b form-lock rejects all deviations.",
				rel,
			),
		}}
	}
	return nil
}

// isCanonicalMismatchExpr verifies expr is exactly:
//
//	callerID == "" || ownerID(resource) != callerID
//
// with operand identities equal to the supplied param names.
func isCanonicalMismatchExpr(expr ast.Expr, resourceP, ownerIDP, callerIDP string) bool {
	lor, ok := expr.(*ast.BinaryExpr)
	if !ok || lor.Op.String() != "||" {
		return false
	}
	// Left side: callerID == ""
	emptyCheck, ok := lor.X.(*ast.BinaryExpr)
	if !ok || emptyCheck.Op.String() != "==" {
		return false
	}
	if !isIdentNamed(emptyCheck.X, callerIDP) {
		return false
	}
	lit, ok := emptyCheck.Y.(*ast.BasicLit)
	if !ok || lit.Value != `""` {
		return false
	}
	// Right side: ownerID(resource) != callerID
	mismatch, ok := lor.Y.(*ast.BinaryExpr)
	if !ok || mismatch.Op.String() != "!=" {
		return false
	}
	call, ok := mismatch.X.(*ast.CallExpr)
	if !ok || !isIdentNamed(call.Fun, ownerIDP) {
		return false
	}
	if len(call.Args) != 1 || !isIdentNamed(call.Args[0], resourceP) {
		return false
	}
	if !isIdentNamed(mismatch.Y, callerIDP) {
		return false
	}
	return true
}

// flattenParamIdents collects the parameter Idents from a FuncDecl param
// FieldList into a positional slice. Each Field may declare multiple names
// (e.g. `a, b int`); each contributes one entry per name.
func flattenParamIdents(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var names []string
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

// checkFunnelReturnForms (B2 supplement) reports a diagnostic for every
// non-nil return value in the CheckOwner body that is NOT the canonical
// `errcode.New(errcode.KindNotFound, _, _)` call. Combined with the
// count==1 + no-other-Kind checks above, this proves all non-nil exits
// from CheckOwner are the IDOR-safe 404 collapse form (no return-path
// bypass possible).
func checkFunnelReturnForms(pass *Pass, fn *ast.FuncDecl, rel string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.ReturnStmt](fn.Body, func(ret *ast.ReturnStmt) {
		// SCANNER-FRAMEWORK-USAGE-01 Path B compliance: replace the original
		// `for _, expr := range ret.Results { expr.(*ast.CallExpr) }` form
		// with three depth-1 typed walks + total-count reconciliation.
		nilCount := 0
		callExprCount := 0

		// (1) Count direct-child Idents whose name == "nil" — the bare nil
		//     literal form in a return statement.
		EachInChildren[ast.Ident](ret, func(id *ast.Ident) {
			if id.Name == "nil" {
				nilCount++
			}
		})

		// (2) For each direct-child CallExpr, emit the original
		//     "not errcode.New" branch when applicable.
		EachInChildren[ast.CallExpr](ret, func(call *ast.CallExpr) {
			callExprCount++
			line := pass.Fset.Position(call.Pos()).Line
			if !isErrCodeNewCall(pass.TypesInfo, call) {
				diags = append(diags, Diagnostic{
					Rel: rel, Line: line,
					Message: fmt.Sprintf(
						"%s CheckOwner has a non-nil return constructing an "+
							"error via a call other than errcode.New — every "+
							"non-nil exit must be the canonical "+
							"errcode.New(errcode.KindNotFound, ...) call.",
						rel,
					),
				})
				return
			}
			if len(call.Args) == 0 || !isKindNotFoundArg(pass, call.Args[0]) {
				// already covered by the otherCalls (Kind drift) accumulator
				// above with a more specific message; skip to avoid duplicate
				return
			}
		})

		// (3) Total-count reconciliation — direct-child Results that are
		//     neither nil-ident nor CallExpr (e.g., bare variable, BasicLit,
		//     ParenExpr like `return (errcode.New(...))`, UnaryExpr like
		//     `return &someStruct{}` — all non-canonical forms).
		//     Emits a single diagnostic per offending ret since precise per-
		//     expr line would require re-walking the slice, which Path B
		//     forbids. CheckOwner conventionally returns single-line, so
		//     ret.Pos() collapses to the offending expr line in practice.
		otherCount := len(ret.Results) - nilCount - callExprCount
		if otherCount > 0 {
			line := pass.Fset.Position(ret.Pos()).Line
			diags = append(diags, Diagnostic{
				Rel: rel, Line: line,
				Message: fmt.Sprintf(
					"%s CheckOwner has %d non-nil return value(s) that are "+
						"not call expressions — every non-nil exit must be "+
						"the canonical errcode.New(errcode.KindNotFound, "+
						"...) call (IDOR collapse uniqueness).",
					rel, otherCount,
				),
			})
		}
	})
	return diags
}

// isErrCodeNewCall reports whether call resolves via go/types to
// errcode.New from pkg/errcode. Used by B2 funnel body lock where the
// canonical exit is errcode.New(KindNotFound, ...); errcode.Wrap is not
// expected inside the funnel and is treated as a B2 violation if present
// (would still be flagged via the otherCalls / notFoundCalls accounting).
func isErrCodeNewCall(typesInfo *types.Info, call *ast.CallExpr) bool {
	pkg, name, ok := resolveErrcodeCtor(typesInfo, call)
	return ok && pkg == serviceOwnedErrcodePkg && name == "New"
}

// isErrCodeNewOrWrapCall reports whether call resolves via go/types to
// errcode.New OR errcode.Wrap from pkg/errcode. Used by B3 zero-tolerance
// ban which must catch both Kind-bearing constructors in service.go (Wrap
// has the same Kind-first-arg signature as New).
//
// Known blindspot (#1199): local var aliasing `local := errcode.KindNotFound;
// errcode.New(local, ...)` — ResolvePackageRef cannot follow runtime values.
// Detection requires SSA dataflow analysis; tracked at gh #1199 alongside
// the SSA callgraph upgrade.
func isErrCodeNewOrWrapCall(typesInfo *types.Info, call *ast.CallExpr) bool {
	pkg, name, ok := resolveErrcodeCtor(typesInfo, call)
	if !ok || pkg != serviceOwnedErrcodePkg {
		return false
	}
	return name == "New" || name == "Wrap"
}

// resolveErrcodeCtor resolves a call's callee through go/types and returns
// the package path and symbol name. Bare-Ident (dot-import) and SelectorExpr
// (qualified) and IndexExpr (explicit generic, defensive) shapes all
// resolve correctly.
func resolveErrcodeCtor(typesInfo *types.Info, call *ast.CallExpr) (pkgPath, name string, ok bool) {
	ref := unwrapCalleeForResolve(call.Fun)
	if ref == nil {
		return "", "", false
	}
	return ResolvePackageRef(typesInfo, ref)
}

// isKindNotFoundArg reports whether arg evaluates to errcode.KindNotFound,
// covering three resolution paths:
//
//  1. Constant value comparison via go/types constant folding — catches
//     `const k = errcode.KindNotFound; errcode.New(k, ...)` (typed const
//     alias). Types[arg].Value resolves to the integer constant value of
//     errcode.KindNotFound through go/types' constant folding, which we
//     compare against the actual value looked up from the pkg/errcode
//     types.Package.
//
//  2. Package-ref resolution via ResolvePackageRef — catches direct
//     `errcode.KindNotFound` (qualified SelectorExpr) and bare
//     `KindNotFound` after dot-import.
//
// Known blindspot (#1199): runtime-var aliasing `local :=
// errcode.KindNotFound; errcode.New(local, ...)` is NOT caught — local
// is a *types.Var, not a *types.Const, so Types[local].Value is nil and
// ResolvePackageRef returns false (local is not a package-level symbol).
// SSA dataflow is required to follow runtime variable values; tracked at
// gh #1199 alongside the B1 callgraph upgrade.
func isKindNotFoundArg(pass *Pass, arg ast.Expr) bool {
	// 1. Constant folding — covers typed const aliases.
	if tv, ok := pass.TypesInfo.Types[arg]; ok && tv.Value != nil {
		if expected := lookupErrcodeKindNotFoundValue(pass); expected != nil {
			if constant.Compare(tv.Value, token.EQL, expected) {
				return true
			}
		}
	}
	// 2. Package-ref resolution — covers direct and dot-import forms.
	pkgPath, name, ok := ResolvePackageRef(pass.TypesInfo, arg)
	if !ok {
		return false
	}
	return pkgPath == serviceOwnedErrcodePkg && name == serviceOwnedKindNotFoundSym
}

// lookupErrcodeKindNotFoundValue returns the constant.Value of
// errcode.KindNotFound by walking pass.Pkg's transitive imports until it
// finds the pkg/errcode types.Package. Returns nil when pkg/errcode is
// not imported by the current pass (in which case isKindNotFoundArg's
// const-folding branch is skipped — the package-ref branch handles
// direct references when the import IS present).
//
// Implementation note: types.Package.Imports() returns the direct
// imports of pass.Pkg. pkg/errcode is a leaf utility package that any
// package using errcode.* will import directly, so walking direct
// imports suffices.
func lookupErrcodeKindNotFoundValue(pass *Pass) constant.Value {
	if pass.Pkg == nil {
		return nil
	}
	for _, imp := range pass.Pkg.Imports() {
		if imp.Path() != serviceOwnedErrcodePkg {
			continue
		}
		obj := imp.Scope().Lookup(serviceOwnedKindNotFoundSym)
		if c, ok := obj.(*types.Const); ok {
			return c.Val()
		}
	}
	return nil
}

// fileCallsAuthCheckOwnerFromEntries reports whether file contains a
// reachable call to runtime/auth.CheckOwner starting from FuncDecls whose
// names appear in entries (resolved from sibling handler.go adapter
// calls). This is a call-graph reachability check, not a bare AST-
// existence scan: dead code at file scope (`var _ =
// auth.CheckOwner[any]{...}`), unreachable unexported helpers no entry
// transitively calls, and exported methods that are NOT in the
// adapter-resolved entry set do not satisfy this predicate.
//
// Entry set = the subset of intra-file FuncDecls whose names appear in
// entries (the handler-adapter-resolved Service method names). False
// negative implication: if the canonical Service struct holds multiple
// owner-scoped methods and the handler adapter only invokes a subset for
// a given contract, only the invoked subset's reachability is checked.
// This is the intended precision: B1 binds to *the contract's actual
// execution path*, not "any exported method of the service".
//
// Closure expansion walks CallExpr nodes in each visited body. A call
// follows an edge into `allFuncs` (intra-file FuncDecls keyed by name)
// when the callee identifier matches; calls to external packages or to
// methods on non-Service types are not followed (closed-world over the
// file). Same-name collisions between local helpers and stdlib symbols
// over-expand the closure but never miss CheckOwner.
//
// Three AST shapes for the CheckOwner callee are recognized via
// unwrapCalleeForResolve: SelectorExpr (`auth.CheckOwner`), IndexExpr
// (`auth.CheckOwner[T]`), IndexListExpr (defensive multi-type-arg).
// Bare-Ident `CheckOwner(...)` after a dot-import resolves through
// ResolvePackageRef and is also caught.
func fileCallsAuthCheckOwnerFromEntries(typesInfo *types.Info, file *ast.File, entries map[string]bool) bool {
	allFuncs := map[string]*ast.FuncDecl{}
	var entry []*ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil {
			return
		}
		allFuncs[fd.Name.Name] = fd
		if entries[fd.Name.Name] {
			entry = append(entry, fd)
		}
	})
	if len(entry) == 0 {
		// adapter resolved to method names that don't exist in this file;
		// either the file was wrong or the adapter wiring is broken. Either
		// way, B1 should fire.
		return false
	}

	visited := map[string]bool{}
	queue := append([]*ast.FuncDecl{}, entry...)
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		if fn.Name == nil || visited[fn.Name.Name] || fn.Body == nil {
			continue
		}
		visited[fn.Name.Name] = true

		found := false
		EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			if found {
				return
			}
			ref := unwrapCalleeForResolve(call.Fun)
			if ref == nil {
				return
			}
			// Check if this is auth.CheckOwner — terminal success
			if pkgPath, name, ok := ResolvePackageRef(typesInfo, ref); ok {
				if pkgPath == serviceOwnedAuthPkg && name == serviceOwnedCheckOwnerSym {
					found = true
					return
				}
			}
			// Otherwise expand BFS into intra-file callees by name
			calleeName := calleeIdentName(ref)
			if calleeName == "" {
				return
			}
			if next, ok := allFuncs[calleeName]; ok && !visited[calleeName] {
				queue = append(queue, next)
			}
		})
		if found {
			return true
		}
	}
	return false
}

// calleeIdentName extracts the function/method name from a callee
// expression (SelectorExpr.Sel.Name or bare Ident.Name) for intra-file
// BFS expansion. Returns empty when the callee shape is not a plain
// name-bearing reference.
func calleeIdentName(ref ast.Expr) string {
	switch v := ref.(type) {
	case *ast.SelectorExpr:
		if v.Sel != nil {
			return v.Sel.Name
		}
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// unwrapCalleeForResolve strips IndexExpr / IndexListExpr wrappers added by
// explicit generic type arguments so the underlying SelectorExpr / Ident can
// be passed to ResolvePackageRef.
//
// IndexExpr handles single-type-arg generic dispatch (`CheckOwner[T](...)`,
// the only currently-instantiable form for `CheckOwner[T any]`). IndexListExpr
// is defensive coverage for future multi-type-arg signatures (e.g.
// `CheckOwner[T, U]`) — Go parses `Foo[X, Y]` as IndexListExpr. The current
// CheckOwner declares a single type parameter, so production code never
// produces IndexListExpr; the branch is defensive only.
func unwrapCalleeForResolve(fun ast.Expr) ast.Expr {
	switch v := fun.(type) {
	case *ast.SelectorExpr, *ast.Ident:
		return v
	case *ast.IndexExpr:
		return unwrapCalleeForResolve(v.X)
	case *ast.IndexListExpr:
		return unwrapCalleeForResolve(v.X)
	}
	return nil
}
