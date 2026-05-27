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
//   - **B1 downstream callsite lock** — Hard. Each serviceOwned slice's
//     service.go must contain ≥1 CallExpr resolving via types.Info to
//     runtime/auth.CheckOwner. Resolution is package-path-bound through
//     archtest.ResolvePackageRef, so import aliasing / dot-import does
//     not evade. Form uniqueness: a same-named "CheckOwner" in a
//     different package fails the path check.
//
//   - **B2 funnel body lock** — Hard. The single production CheckOwner
//     function in runtime/auth/owner_guard.go must contain exactly one
//     errcode.New call whose first argument type-resolves to KindNotFound,
//     and zero errcode.New calls with any other Kind. Drift to
//     KindPermissionDenied / KindForbidden / additional New calls is
//     immediately detected.
//
//   - **B3 upstream zero-tolerance ban** — Hard. service.go files in
//     serviceOwned slices must contain zero errcode.New(KindNotFound, ...)
//     callsites. Detection is a single-assertion count == 0 with no AST
//     form matching, no carve-out, and no cross-function helper escape:
//     wrapping the raw errcode.New in a helper still counts toward the
//     production scope. Lookup-failure paths must collapse through the
//     funnel via nil-safe accessors (sessionlogout/service.go canonical
//     form), preserving the IDOR-safe 404 collapse semantics. The
//     funnel's own body lives in runtime/, not cells/, and is therefore
//     out of B3 scope by construction.
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
// loads six testdata packages with full types.Info via archtest.RunTyped,
// sharing the same predicate closures as the production scans. Each
// predicate is exercised on both a green path (silent) and one or more red
// paths (firing), and cross-predicate silence is asserted on red fixtures
// targeting other predicates (B1 RED fixtures must be silent on B3, etc.)
// to keep B1/B2/B3 independently distinguishable.
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
	"go/types"
	"os"
	"path/filepath"
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
			for _, file := range pass.Files {
				rel := pass.Rel(file)
				seenRels[rel] = true

				// B2: funnel body check on owner_guard.go
				if rel == serviceOwnedOwnerGuardFile {
					d = append(d, checkFunnelBody(pass, file, rel)...)
					continue
				}

				// B1+B3: per serviceOwned service.go target
				var matched *target
				for i := range targets {
					if targets[i].rel == rel {
						matched = &targets[i]
						break
					}
				}
				if matched == nil {
					continue
				}
				d = append(d, checkServiceFileCalleeLock(pass, file, rel, matched.contractID)...)
				d = append(d, checkServiceFileZeroToleranceBan(pass, file, rel, matched.contractID)...)
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
//   - red_funnel_body_wrong_kind/owner_guard.go: CheckOwner returns
//     KindPermissionDenied → B2 fires
//   - red_funnel_body_extra_new/owner_guard.go: CheckOwner has two errcode.New
//     calls → B2 fires
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
		// B2 fixtures (owner_guard.go scope) — green + red
		{subdir: "green_funnel_body", pred: predB2, wantViolations: false},
		{subdir: "red_funnel_body_wrong_kind", pred: predB2, wantViolations: true},
		{subdir: "red_funnel_body_extra_new", pred: predB2, wantViolations: true},
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
					for _, file := range pass.Files {
						rel := pass.Rel(file)
						switch tc.pred {
						case predB1:
							d = append(d, checkServiceFileCalleeLock(pass, file, rel, "fixture-contract")...)
						case predB2:
							d = append(d, checkFunnelBody(pass, file, rel)...)
						case predB3:
							d = append(d, checkServiceFileZeroToleranceBan(pass, file, rel, "fixture-contract")...)
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

// checkServiceFileCalleeLock (B1) reports a diagnostic if file contains zero
// CallExpr whose callee type-resolves to runtime/auth.CheckOwner.
func checkServiceFileCalleeLock(pass *Pass, file *ast.File, rel, contractID string) []Diagnostic {
	if fileCallsAuthCheckOwner(pass.TypesInfo, file) {
		return nil
	}
	return []Diagnostic{{
		Rel: rel, Line: 0,
		Message: fmt.Sprintf(
			"contract %q (auth.serviceOwned=true) serving slice in %s "+
				"is missing an auth.CheckOwner call. Service-layer "+
				"ownership checks must go through the CheckOwner funnel "+
				"(see runtime/auth/owner_guard.go). Canonical form: "+
				"cells/accesscore/slices/sessionlogout/service.go "+
				"(Service.Logout revokeAndPublish closure) — note the "+
				"nil-safe accessor pattern collapsing lookup-failure into "+
				"the funnel. B3 separately enforces zero raw "+
				"errcode.New(errcode.KindNotFound, ...) calls in this file.",
			contractID, rel,
		),
	}}
}

// checkServiceFileZeroToleranceBan (B3) reports a diagnostic for every
// errcode.New(errcode.KindNotFound, ...) call in file. Zero is the only
// passing count.
func checkServiceFileZeroToleranceBan(pass *Pass, file *ast.File, rel, contractID string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isErrCodeNewCall(pass.TypesInfo, call) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		if !isKindNotFoundArg(pass.TypesInfo, call.Args[0]) {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: pass.Fset.Position(call.Pos()).Line,
			Message: fmt.Sprintf(
				"contract %q (auth.serviceOwned=true): %s contains a raw "+
					"errcode.New(errcode.KindNotFound, ...) call. All "+
					"KindNotFound returns in serviceOwned service.go must "+
					"come from the runtime/auth.CheckOwner funnel "+
					"(zero-tolerance ban — IDOR collapse depends on a "+
					"single sanctioned funnel body). Fix: delete this "+
					"errcode.New call and let the nil-safe accessor (returns "+
					"\"\" on nil/not-found resource) collapse lookup-failure "+
					"into the funnel. See testdata fixtures green/ (correct "+
					"form) and red_raw_kindnotfound_in_service/ (anti-pattern).",
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
		if isKindNotFoundArg(pass.TypesInfo, call.Args[0]) {
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
	return diags
}

// isErrCodeNewCall reports whether call resolves via go/types to
// errcode.New from pkg/errcode. Both name and package path are verified;
// a same-named "New" function in any other package fails the check, closing
// the blindspot that would exist if only the selector name were matched.
func isErrCodeNewCall(typesInfo *types.Info, call *ast.CallExpr) bool {
	ref := unwrapCalleeForResolve(call.Fun)
	if ref == nil {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(typesInfo, ref)
	if !ok {
		return false
	}
	return pkgPath == serviceOwnedErrcodePkg && name == "New"
}

// isKindNotFoundArg reports whether arg resolves via go/types to
// errcode.KindNotFound from pkg/errcode.
func isKindNotFoundArg(typesInfo *types.Info, arg ast.Expr) bool {
	pkgPath, name, ok := ResolvePackageRef(typesInfo, arg)
	if !ok {
		return false
	}
	return pkgPath == serviceOwnedErrcodePkg && name == serviceOwnedKindNotFoundSym
}

// fileCallsAuthCheckOwner reports whether file contains at least one CallExpr
// whose callee type-resolves via go/types to runtime/auth.CheckOwner.
//
// Handles three AST shapes for the callee:
//   - `auth.CheckOwner(...)` — SelectorExpr (type inferred)
//   - `auth.CheckOwner[T](...)` — IndexExpr wrapping SelectorExpr (explicit single type arg)
//   - `auth.CheckOwner[T, U](...)` — IndexListExpr wrapping SelectorExpr (multiple type args)
//
// Bare-Ident `CheckOwner(...)` after a dot-import also resolves correctly.
func fileCallsAuthCheckOwner(typesInfo *types.Info, file *ast.File) bool {
	found := false
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if found {
			return
		}
		ref := unwrapCalleeForResolve(call.Fun)
		if ref == nil {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(typesInfo, ref)
		if !ok {
			return
		}
		if pkgPath == serviceOwnedAuthPkg && name == serviceOwnedCheckOwnerSym {
			found = true
		}
	})
	return found
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
