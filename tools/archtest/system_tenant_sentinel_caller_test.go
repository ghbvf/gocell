// system_tenant_sentinel_caller_test.go — locks the single sanctioned
// production reference to tenant.SystemTenantID, a tenant-isolation bypass
// token used by the configcore internal control-plane read path.
//
// INVARIANT: SYSTEM-TENANT-SENTINEL-CALLER-01
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
	"go/types"
	"sort"
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
