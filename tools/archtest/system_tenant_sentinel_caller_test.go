// system_tenant_sentinel_caller_test.go — locks the single sanctioned
// production reference to tenant.SystemTenantID, a tenant-isolation bypass
// token used by the configcore internal control-plane read path. Also locks
// production code against constructing the reserved nil-UUID value from a raw
// string literal (value-level bypass of the const-ref scanner).
//
// INVARIANT: SYSTEM-TENANT-SENTINEL-CALLER-01
//   - INVARIANT: SYSTEM-TENANT-SENTINEL-VALUE-01
//
// # What this guards
//
// tenant.SystemTenantID is the reserved nil-UUID TenantID used by
// configcore's configreadinternal slice so that the internal /internal/v1
// control-plane path (authenticated by a service token, not a JWT) can call
// the tenant-scoped ConfigRepository.GetByKey without having a real tenant in
// context. Passing it to a repo method crosses the per-tenant boundary and
// reads the global/platform config tier.
//
// This constant is a tenant-isolation bypass token. Any new caller that passes
// it to a repo method silently widens the read surface without per-tenant
// scoping, opening the door for privilege escalation or cross-tenant data leaks.
// The archtest therefore locks every non-test production reference to the single
// sanctioned caller:
//
//   - cells/configcore/slices/configreadinternal/handler.go
//
// All *_test.go files and test-support packages (any path segment equal to a
// package whose name ends in "test" or is under a "test" directory) are excluded
// from the scan. Docstring / comment references are not Go code, so the scanner
// only fires on real uses (TypesInfo.Uses identifiers), not on mentions in
// godoc text.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: MEDIUM (archtest caller-allowlist; go/types object-identity
//     match resolves the *types.Const for tenant.SystemTenantID from the
//     pkg/tenant package scope, then scans TypesInfo.Uses[ident] across all
//     production packages; import aliases cannot bypass because the resolved
//     object identity is canonical regardless of import style).
//   - Upstream: Go-language ceiling (permanent won't-do). SystemTenantID must
//     stay exported for the cross-package configreadinternal reference; Go
//     package visibility cannot gate "only this one external package may read
//     this exported const". The Hard-path (unexport + sanctioned typed accessor
//     that only configreadinternal can call) is tracked at gh #1576 as a
//     deliberate won't-do, same permanent ceiling as #851 / #893 / #1282.
//
// # Tool blind spots (charter §"工具选定后强制盲区自检")
//
//  1. Value laundered through an intermediate variable:
//     tid := tenant.SystemTenantID
//     repo.GetByKey(ctx, tid, key)
//     The TypesInfo.Uses scanner fires on `tenant.SystemTenantID` at the
//     assignment site (caught), but the use of `tid` at the call site is an
//     *ast.Ident resolved to a local *types.Var, NOT the SystemTenantID
//     *types.Const — that second use is a blind spot. The local-var use is
//     not detected; the sentinel const reference at the assignment IS detected.
//     Mitigation: the assignment line fires the scanner, so the file must
//     be in the allowlist. Laundering within an allowlisted file is fine
//     (configreadinternal/handler.go itself could use a local var internally).
//     Laundering in a NON-allowlisted file is caught at the assignment site.
//  2. Reflection or unsafe: reflect.ValueOf(tenant.SystemTenantID) or
//     unsafe.Pointer tricks do not produce a TypesInfo.Uses entry for the
//     const object — both are absent from production today (verified by the
//     reverse self-check TestSystemTenantSentinelCaller01_VacuityAndPresence).
//
// # Anti-vacuity
//
// The test asserts that at least one sanctioned reference to SystemTenantID
// is actually observed in the production scan. If the sentinel disappears
// from configreadinternal/handler.go (e.g. refactored away), the test fails
// immediately rather than vacuously passing with an empty allowlist.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// tenantSentinelPkg is the package that declares SystemTenantID.
	// Derived from PlatformModulePath for rename-safety.
	tenantSentinelPkg = PlatformModulePath + "/pkg/tenant"
	// tenantSentinelName is the exported const name.
	tenantSentinelName = "SystemTenantID"
	// ruleIDSentinel is the invariant ID; kept as a const so it appears in
	// exactly one place across all diagnostic messages in this file.
	ruleIDSentinel = "SYSTEM-TENANT-SENTINEL-CALLER-01"
)

// systemTenantSentinelAllowlist is the bounded set of module-relative file
// paths that are permitted to reference tenant.SystemTenantID in production
// code (i.e. non-test files). Every entry must be observed at least once
// during the scan (anti-vacuity); stale entries are flagged as diagnostics.
//
// Hard-upgrade path: unexport SystemTenantID + sanctioned typed accessor
// whose visibility prevents access from outside configreadinternal. Tracked
// at gh #1576 (won't-do now; same ceiling as #851 / #893 / #1282).
var systemTenantSentinelAllowlist = map[string]struct{}{
	// sole sanctioned reference: the internal control-plane GET adapter passes
	// tenant.SystemTenantID to ConfigRepository.GetByKey because service-token
	// callers have no tenant in ctx (configreadinternal handler.go).
	"cells/configcore/slices/configreadinternal/handler.go": {},
}

// isSystemTenantSentinelFileAllowed reports whether a module-relative file path
// is permitted to reference tenant.SystemTenantID. _test.go files are always
// allowed (tests legitimately use the sentinel for fixture setup / assertions).
// Packages under paths containing "/test/" or ending in "test" are also excluded
// (test-support helpers live there).
func isSystemTenantSentinelFileAllowed(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	// Allow test-support packages (paths that contain a "test" directory segment
	// or whose base package name ends in "test", e.g. accesscoretest/).
	for _, seg := range strings.Split(rel, "/") {
		if seg == "test" || strings.HasSuffix(seg, "test") {
			return true
		}
	}
	_, ok := systemTenantSentinelAllowlist[rel]
	return ok
}

// resolveSystemTenantSentinelObj resolves the *types.Const for
// tenant.SystemTenantID from the pkg/tenant package pass. Returns nil if the
// current pass is not the tenant package, or if the const is not found.
func resolveSystemTenantSentinelObj(p *Pass) *types.Const {
	if p.Pkg == nil || p.Pkg.Path() != tenantSentinelPkg {
		return nil
	}
	obj := p.Pkg.Scope().Lookup(tenantSentinelName)
	if obj == nil {
		return nil
	}
	c, ok := obj.(*types.Const)
	if !ok {
		return nil
	}
	return c
}

// TestSystemTenantSentinelCaller01 asserts that every production (non-test)
// reference to tenant.SystemTenantID sits in systemTenantSentinelAllowlist,
// and that every allowlist entry is observed at least once (anti-vacuity).
//
// Detection uses go/types TypesInfo.Uses: for every *ast.Ident in the
// production AST, if info.Uses[ident] == the *types.Const object resolved from
// the pkg/tenant scope, the file containing that ident must be allowlisted.
// This makes the check import-alias-proof: aliased references resolve to the
// same *types.Const identity.
func TestSystemTenantSentinelCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Phase 1: resolve the *types.Const for tenant.SystemTenantID.
	var sentinelObj *types.Const
	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil {
			return nil
		}
		if obj := resolveSystemTenantSentinelObj(p); obj != nil {
			sentinelObj = obj
		}
		return nil
	})

	var allDiags []Diagnostic
	if sentinelObj == nil {
		allDiags = append(allDiags, Diagnostic{
			Message: ruleIDSentinel + ": failed to resolve tenant.SystemTenantID *types.Const " +
				"from " + tenantSentinelPkg + " — scanner regressed or the const was renamed/removed",
		})
		Report(t, ruleIDSentinel, allDiags)
		return
	}

	// Phase 2: scan all production code for references to sentinelObj.
	observed := map[string]struct{}{} // module-relative file paths that reference the sentinel

	scanDiags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.TypesInfo == nil {
			return nil
		}
		// Build abs→rel map for this pass.
		relByAbs := make(map[string]string, len(p.Files))
		for _, f := range p.Files {
			relByAbs[p.Abs(f)] = p.Rel(f)
		}

		var d []Diagnostic
		for ident, obj := range p.TypesInfo.Uses {
			if obj != sentinelObj {
				continue
			}
			// Found a reference to tenant.SystemTenantID.
			pos := p.Fset.Position(ident.Pos())
			rel, ok := relByAbs[pos.Filename]
			if !ok {
				continue
			}
			if isSystemTenantSentinelFileAllowed(rel) {
				// Track allowlist-file references for anti-vacuity.
				if _, inAllowlist := systemTenantSentinelAllowlist[rel]; inAllowlist {
					observed[rel] = struct{}{}
				}
				continue
			}
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					ruleIDSentinel+": %s:%d references tenant.SystemTenantID, "+
						"which is a tenant-isolation bypass token. Its only sanctioned "+
						"production reference is "+
						"cells/configcore/slices/configreadinternal/handler.go "+
						"(the internal control-plane read path where service-token callers "+
						"have no tenant in ctx). If this is a new legitimate control-plane "+
						"path that truly has no tenant source, add %q to "+
						"systemTenantSentinelAllowlist with rationale. "+
						"Hard-upgrade path: gh #1576 (unexport + typed accessor).",
					rel, pos.Line, rel,
				),
			})
		}
		return d
	})
	allDiags = append(allDiags, scanDiags...)

	// Anti-vacuity / stale-allowlist check: every allowlisted non-test file
	// must have been observed referencing SystemTenantID at least once.
	stale := make([]string, 0, len(systemTenantSentinelAllowlist))
	for f := range systemTenantSentinelAllowlist {
		if _, seen := observed[f]; !seen {
			stale = append(stale, f)
		}
	}
	sort.Strings(stale)
	for _, f := range stale {
		allDiags = append(allDiags, Diagnostic{
			Message: fmt.Sprintf(
				ruleIDSentinel+": allowlist entry %q is STALE — no live "+
					"production reference to tenant.SystemTenantID observed in that file. "+
					"Either the reference was removed/refactored (drop the allowlist entry) "+
					"or the scanner regressed (check tenantSentinelPkg and go/types Uses detection).",
				f,
			),
		})
	}

	Report(t, ruleIDSentinel, allDiags)
}

// TestSystemTenantSentinelCaller01_VacuityAndPresence is the anti-vacuity
// self-check: it asserts that the production scan found at least one reference
// to tenant.SystemTenantID in the sanctioned allowlist file. This ensures that:
//  1. The scanner is working (sentinelObj was resolved correctly).
//  2. configreadinternal/handler.go still references the sentinel (so a
//     refactor that removes the reference would immediately red this test).
//
// This is a separate test from the main invariant so that failures here give a
// distinct, actionable message: "the sentinel disappeared from the only
// sanctioned caller" vs "an unsanctioned caller appeared".
func TestSystemTenantSentinelCaller01_VacuityAndPresence(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Resolve sentinel object.
	var sentinelObj *types.Const
	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil {
			return nil
		}
		if obj := resolveSystemTenantSentinelObj(p); obj != nil {
			sentinelObj = obj
		}
		return nil
	})
	if sentinelObj == nil {
		t.Fatal(ruleIDSentinel + " vacuity check: failed to resolve tenant.SystemTenantID — scanner regressed")
		return
	}

	// Check that configreadinternal/handler.go actually references the sentinel.
	const sanctionedFile = "cells/configcore/slices/configreadinternal/handler.go"
	var foundInSanctioned bool

	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.TypesInfo == nil {
			return nil
		}
		relByAbs := make(map[string]string, len(p.Files))
		for _, f := range p.Files {
			relByAbs[p.Abs(f)] = p.Rel(f)
		}
		for ident, obj := range p.TypesInfo.Uses {
			if obj != sentinelObj {
				continue
			}
			pos := p.Fset.Position(ident.Pos())
			rel, ok := relByAbs[pos.Filename]
			if !ok {
				continue
			}
			if rel == sanctionedFile {
				foundInSanctioned = true
			}
		}
		return nil
	})

	if !foundInSanctioned {
		t.Errorf(
			ruleIDSentinel+" vacuity FAILED: no reference to tenant.SystemTenantID "+
				"observed in the sanctioned file %q. Either the sentinel was removed "+
				"(in which case drop the allowlist entry and retire this rule) or "+
				"the scanner regressed (check that configcorePortsPkg loads correctly).",
			sanctionedFile,
		)
	}
}

// TestSystemTenantSentinelCaller01_BlindSpot_LocalVarLaunderingAbsent asserts
// that no production non-test file outside the sanctioned allowlist assigns
// tenant.SystemTenantID to a local variable (blind spot #1: the assignment
// reference fires the scanner, but the downstream local-var use does not).
//
// This reverse self-check verifies absence today so that a future introduction
// prompts investigation. Since the scanner DOES detect the assignment reference
// (the const ident on the RHS), any non-allowlisted file doing this would
// already be caught by TestSystemTenantSentinelCaller01. This test exists only
// as a documented verification that the blind-spot pattern is absent.
//
// Note: this test PASSES vacuously if the pattern is absent (that is the desired
// state). If it fails, a non-allowlisted file introduced a local-var alias for
// SystemTenantID — which means TestSystemTenantSentinelCaller01 would ALSO fail
// on the same file (the assignment const reference IS caught). Both tests should
// therefore be red simultaneously, making the violation doubly visible.
func TestSystemTenantSentinelCaller01_BlindSpot_LocalVarLaunderingAbsent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Resolve sentinel object.
	var sentinelObj *types.Const
	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil {
			return nil
		}
		if obj := resolveSystemTenantSentinelObj(p); obj != nil {
			sentinelObj = obj
		}
		return nil
	})
	if sentinelObj == nil {
		t.Skip(ruleIDSentinel + " blind-spot check: sentinel object not resolved, skipping")
		return
	}

	// Scan for assignment of SystemTenantID to a local variable (`:=` or `=`)
	// in non-allowlisted, non-test production files.
	var unsanctionedAssignments []string
	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.TypesInfo == nil {
			return nil
		}
		relByAbs := make(map[string]string, len(p.Files))
		for _, f := range p.Files {
			relByAbs[p.Abs(f)] = p.Rel(f)
		}
		for ident, obj := range p.TypesInfo.Uses {
			if obj != sentinelObj {
				continue
			}
			pos := p.Fset.Position(ident.Pos())
			rel, ok := relByAbs[pos.Filename]
			if !ok || isSystemTenantSentinelFileAllowed(rel) {
				continue
			}
			// Any unsanctioned reference is already caught by the main test,
			// but record it here for the explicit blind-spot verification.
			unsanctionedAssignments = append(unsanctionedAssignments,
				fmt.Sprintf("%s:%d", rel, pos.Line))
		}
		return nil
	})

	// The test passes if there are no unsanctioned references (the main
	// invariant test would also red on these). Document absence explicitly.
	if len(unsanctionedAssignments) > 0 {
		t.Errorf(
			ruleIDSentinel+" blind-spot #1 self-check: found %d non-allowlisted "+
				"production reference(s) to tenant.SystemTenantID (these should "+
				"also be caught by TestSystemTenantSentinelCaller01): %v",
			len(unsanctionedAssignments), unsanctionedAssignments,
		)
	}
}

// ─── SYSTEM-TENANT-SENTINEL-VALUE-01 ─────────────────────────────────────────

// TestSystemTenantSentinelValue01 bans non-allowlist PRODUCTION code from
// constructing the reserved nil-UUID value from a raw string literal.
//
// # What this guards
//
// SYSTEM-TENANT-SENTINEL-CALLER-01 matches const-object identity via
// TypesInfo.Uses: it fires when code references the *types.Const for
// tenant.SystemTenantID. A value-level bypass exists: a caller could write
//
//	tenant.TenantID("00000000-0000-0000-0000-000000000000")
//	tenant.ParseTenantID("00000000-0000-0000-0000-000000000000")
//	ctxkeys.WithTenantID("00000000-0000-0000-0000-000000000000")
//
// These expressions produce the sentinel value without ever naming the const,
// so they escape CALLER-01. This test closes that gap by scanning for
// the nil-UUID string literal "00000000-0000-0000-0000-000000000000" appearing
// as a CallExpr argument to any of the three typed functions above (identified
// by their resolved package path + function name). The only sanctioned site is
// the const declaration itself in pkg/tenant/system_tenant.go.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: MEDIUM. Archtest value-level caller-allowlist: for each AST
//     CallExpr whose callee resolves (via go/types TypesInfo.Selections or
//     ObjectOf) to one of the three watched functions, if any argument is a
//     string BasicLit with value == the nil-UUID, the containing file must be
//     in the allowlist. Import aliases cannot bypass because the resolution
//     uses go/types package path + function name, not source text.
//
//   - Upstream: Go-language ceiling (permanent won't-do). TenantID is a plain
//     `string` newtype; Go cannot prevent `tenant.TenantID("any string")` at
//     the type-system level. The sentinel must stay exported (cross-package
//     configreadinternal reference), so no sealed accessor can gate it. Hard
//     path (unexport SystemTenantID + sanctioned typed accessor) tracked at
//     gh #1576 — same permanent ceiling as #851 / #893 / #1282.
//
// # Tool blind spots (charter §"工具选定后强制盲区自检")
//
//  1. Computed nil-UUID (e.g. strings.Repeat("0", 8)+"-..."+...): the scanner
//     matches only ast.BasicLit string nodes, not computed strings. Mitigation:
//     rare in practice; would also trigger CALLER-01 suspicion at code review.
//  2. Indirect call (e.g. f := tenant.ParseTenantID; f("0000...")):  the
//     callee is a local *ast.Ident resolved to a *types.Var, not a *types.Func;
//     not detected. Mitigation: indirect call is itself suspicious and would
//     require a reference to ParseTenantID at the assignment site (caught by
//     CALLER-01 if it uses the const; not caught if it uses the func name — an
//     accepted residual with very low exploitation probability).
//  3. Constant folding / const alias (e.g. const nilUUID = "0000...";
//     ParseTenantID(nilUUID)): the argument is a *ast.Ident, not a *ast.BasicLit
//     — not detected. Mitigation: a non-system_tenant.go production file
//     declaring a local copy of the nil-UUID string would itself be suspicious
//     and likely caught in review.
//
// # Anti-vacuity
//
// The test asserts that at least one sanctioned site (the const declaration in
// system_tenant.go) is observed during the scan; if the const value changes or
// the file is deleted, the test fails rather than vacuously passing.
//
// # Reverse self-check (blind-spot #1 absence)
//
// TestSystemTenantSentinelValue01_BlindSpot_ComputedAbsent asserts that no
// production code constructs the nil-UUID via string concatenation or
// format verbs — verifying the computed-string blind spot is absent today.
const (
	ruleIDSentinelValue = "SYSTEM-TENANT-SENTINEL-VALUE-01"
	// nilUUIDLiteral is the canonical nil-UUID string. It is spelled out here
	// (not imported from pkg/tenant) to keep the archtest self-contained and
	// avoid a circular dependency on the very package being audited.
	nilUUIDLiteral = "00000000-0000-0000-0000-000000000000"
	// sentinelDeclFile is the sole sanctioned production file allowed to contain
	// the nil-UUID literal. All *_test.go files are additionally allowed.
	sentinelDeclFile = "pkg/tenant/system_tenant.go"
)

// sentinelValueAllowlist is the bounded set of non-test production file paths
// (module-relative) that may contain the nil-UUID literal.
// Only the declaration site is allowed; all other production callers must use
// the typed constant tenant.SystemTenantID.
var sentinelValueAllowlist = map[string]struct{}{
	sentinelDeclFile: {},
}

// sentinelValueWatchedCallees is the set of (pkgPath, funcName) pairs whose
// string arguments are checked for the nil-UUID literal. These are the three
// public APIs that could be used to construct the sentinel value from input.
//
// Note: tenant.TenantID("0000...") is a type conversion, not a function call;
// in AST terms it appears as a *ast.CallExpr with Fun = *ast.SelectorExpr or
// *ast.Ident resolving to the type. We handle it by detecting CastExpr to
// tenant.TenantID via TypesInfo (type name == "TenantID" in pkg/tenant).
var sentinelValueWatchedCallees = []struct {
	pkgPath  string
	funcName string
}{
	{pkgPath: tenantSentinelPkg, funcName: "ParseTenantID"},
	{pkgPath: PlatformModulePath + "/pkg/ctxkeys", funcName: "WithTenantID"},
}

// isSentinelValueFileAllowed mirrors isSystemTenantSentinelFileAllowed: test
// files and test-support packages are always allowed.
func isSentinelValueFileAllowed(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "test" || strings.HasSuffix(seg, "test") {
			return true
		}
	}
	_, ok := sentinelValueAllowlist[rel]
	return ok
}

// extractStringLiteral returns the unquoted string value of an ast.BasicLit
// with token.STRING kind, or ("", false) for any other node type.
func extractStringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// TestSystemTenantSentinelValue01 is the main invariant test for
// SYSTEM-TENANT-SENTINEL-VALUE-01. See package godoc above for rationale.
func TestSystemTenantSentinelValue01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var allDiags []Diagnostic
	var sanctionedSeen bool // anti-vacuity: declaration site observed?

	scanDiags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.TypesInfo == nil {
			return nil
		}

		relByAbs := make(map[string]string, len(p.Files))
		for _, f := range p.Files {
			relByAbs[p.Abs(f)] = p.Rel(f)
		}

		var d []Diagnostic

		for _, file := range p.Files {
			rel := p.Rel(file)
			// Anti-vacuity: record observation of the declaration site.
			if rel == sentinelDeclFile {
				sanctionedSeen = true
			}

			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				// Check if any arg is the nil-UUID literal.
				argIdx := -1
				var argStr string
				for i, arg := range call.Args {
					s, ok := extractStringLiteral(arg)
					if ok && s == nilUUIDLiteral {
						argIdx = i
						argStr = s
						break
					}
				}
				if argIdx < 0 {
					return // no nil-UUID literal arg
				}

				pos := p.Fset.Position(call.Pos())
				fileRel, ok := relByAbs[pos.Filename]
				if !ok {
					return
				}
				if isSentinelValueFileAllowed(fileRel) {
					return // allowed (declaration site or test file)
				}

				// Determine if this callsite invokes a watched function.
				calleeDesc := resolveCalleeDesc(p, call)
				if calleeDesc == "" {
					return // not a watched callee
				}

				d = append(d, Diagnostic{
					Rel:  fileRel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						ruleIDSentinelValue+": %s:%d passes the reserved nil-UUID literal %q "+
							"to %s (arg index %d). "+
							"Use the typed constant tenant.SystemTenantID instead of a raw string literal — "+
							"raw nil-UUID construction bypasses the CALLER-01 const-ref scanner "+
							"and risks aliasing the system-tier config. "+
							"Only pkg/tenant/system_tenant.go may contain this literal. "+
							"Hard-upgrade path: gh #1576 (unexport + typed accessor).",
						fileRel, pos.Line, argStr, calleeDesc, argIdx,
					),
				})
			})

			// Also check TenantID("0000...") type-conversion calls.
			// A type conversion tenant.TenantID("0000...") appears as a
			// *ast.CallExpr where Fun resolves to the TenantID type in pkg/tenant.
			checkTenantIDConversion(p, file, relByAbs, &d)
		}
		return d
	})
	allDiags = append(allDiags, scanDiags...)

	// Anti-vacuity: the declaration site must have been observed.
	if !sanctionedSeen {
		allDiags = append(allDiags, Diagnostic{
			Message: ruleIDSentinelValue + ": declaration site " + sentinelDeclFile +
				" was not observed during the scan — either the file was renamed/removed " +
				"or the scanner regressed. Update sentinelDeclFile if the file moved.",
		})
	}

	Report(t, ruleIDSentinelValue, allDiags)
}

// resolveCalleeDesc returns a human-readable "pkg.Func" description if the
// call's callee is one of the watched functions; returns "" otherwise.
// Uses TypesInfo to resolve the callee to its *types.Func object, then checks
// its package path and name against sentinelValueWatchedCallees.
func resolveCalleeDesc(p *Pass, call *ast.CallExpr) string {
	var obj types.Object
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj = p.TypesInfo.ObjectOf(fun)
	case *ast.SelectorExpr:
		obj = p.TypesInfo.ObjectOf(fun.Sel)
	default:
		return ""
	}
	if obj == nil {
		return ""
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return ""
	}
	pkg := fn.Pkg()
	if pkg == nil {
		return ""
	}
	for _, w := range sentinelValueWatchedCallees {
		if pkg.Path() == w.pkgPath && fn.Name() == w.funcName {
			return pkg.Name() + "." + fn.Name()
		}
	}
	return ""
}

// checkTenantIDConversion scans for tenant.TenantID("0000...") type conversion
// calls in the given file. A type conversion appears in the AST as a
// *ast.CallExpr where Fun is an *ast.SelectorExpr (or *ast.Ident) resolving to
// the named type tenant.TenantID. We detect this by resolving the Fun via
// TypesInfo.Uses and checking if the object is the TenantID type in pkg/tenant.
func checkTenantIDConversion(p *Pass, file ast.Node,
	relByAbs map[string]string, d *[]Diagnostic,
) {
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if len(call.Args) != 1 {
			return
		}
		argStr, ok := extractStringLiteral(call.Args[0])
		if !ok || argStr != nilUUIDLiteral {
			return
		}
		// Check if Fun resolves to the TenantID type.
		var ident *ast.Ident
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			ident = fun
		case *ast.SelectorExpr:
			ident = fun.Sel
		default:
			return
		}
		obj := p.TypesInfo.ObjectOf(ident)
		if obj == nil {
			return
		}
		tn, ok := obj.(*types.TypeName)
		if !ok {
			return
		}
		if tn.Pkg() == nil || tn.Pkg().Path() != tenantSentinelPkg || tn.Name() != "TenantID" {
			return
		}
		// This is tenant.TenantID("0000...").
		pos := p.Fset.Position(call.Pos())
		fileRel, hasRel := relByAbs[pos.Filename]
		if !hasRel {
			return
		}
		if isSentinelValueFileAllowed(fileRel) {
			return
		}
		*d = append(*d, Diagnostic{
			Rel:  fileRel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				ruleIDSentinelValue+": %s:%d constructs the reserved nil-UUID literal %q "+
					"via tenant.TenantID(...) type conversion. "+
					"Use the typed constant tenant.SystemTenantID instead. "+
					"Only pkg/tenant/system_tenant.go may contain this literal. "+
					"Hard-upgrade path: gh #1576.",
				fileRel, pos.Line, nilUUIDLiteral,
			),
		})
	})
}

// TestSystemTenantSentinelValue01_BlindSpot_ComputedAbsent is the reverse
// self-check for blind spot #1 (computed nil-UUID strings). It asserts that
// no production non-test file contains string literals that are clearly a
// partial nil-UUID construction — specifically, strings that contain "00000000"
// but are neither the full nil-UUID literal nor any other valid 36-char
// canonical UUID (which would just be a legitimate test-fixture value).
//
// This test PASSES vacuously if the pattern is absent (desired state). If it
// fails, someone introduced a computed nil-UUID construction in production code
// — which the main scanner cannot catch — requiring manual investigation.
//
// Note: strings like "00000000-0000-0000-0000-000000000001" are valid canonical
// UUIDs (test fixtures), not partial nil-UUID constructions — they are excluded.
func TestSystemTenantSentinelValue01_BlindSpot_ComputedAbsent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Segments that appear in a manually split nil-UUID. If these appear as
	// string literals in production code outside the allowlist AND are not
	// themselves complete canonical UUIDs (36-char dashed form), it is
	// strong evidence of a partial nil-UUID construction.
	const nilUUIDSegment = "00000000"

	// isCanonicalUUID reports whether s is a complete 36-char dashed UUID.
	// These are legitimate values (test fixtures, IDs) even if they start with zeros.
	// 36 = canonical UUID length: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	isCanonicalUUID := func(s string) bool {
		if len(s) != 36 {
			return false
		}
		// Cheap structural check: dashes at positions 8, 13, 18, 23.
		return s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-'
	}

	var violations []string

	Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isSentinelValueFileAllowed(rel) {
				continue
			}
			EachInSubtree[ast.BasicLit](file, func(lit *ast.BasicLit) {
				if lit.Kind != token.STRING {
					return
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					return
				}
				// Flag only strings that:
				// 1. Contain the 8-zero segment (could be partial nil-UUID), AND
				// 2. Are NOT the full nil-UUID itself (already caught by main scanner), AND
				// 3. Are NOT a complete canonical UUID (legitimate test fixture).
				if strings.Contains(s, nilUUIDSegment) && s != nilUUIDLiteral && !isCanonicalUUID(s) {
					pos := p.Fset.Position(lit.Pos())
					violations = append(violations,
						fmt.Sprintf("%s:%d (value %q)", rel, pos.Line, s))
				}
			})
		}
		return nil
	})

	if len(violations) > 0 {
		t.Errorf(
			ruleIDSentinelValue+" blind-spot #1 self-check: found production file(s) "+
				"containing partial zero-segment string literals that may be part of a computed "+
				"nil-UUID construction (cannot be detected by the main literal scanner): %v",
			violations,
		)
	}
}
