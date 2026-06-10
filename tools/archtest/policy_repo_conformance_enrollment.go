package archtest

// policy_repo_conformance_enrollment.go — importable detection logic for
// POLICYREPO-CONFORMANCE-ENROLLMENT-01 (#1337 PR-6).
//
// Non-test home of the detector so it can be compiled by an external
// Cell repository (Go never compiles a dependency's _test.go).
// GoCell's own Test* functions in policy_repo_conformance_enrollment_test.go
// call the same Check* — single source, no parallel rule body.
//
// Platform-symbol paths are anchored to [PlatformModulePath].

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

// ─── rule ID constant ──────────────────────────────────────────────────────

const rulePolicyRepoConformanceEnrollment01 = "POLICYREPO-CONFORMANCE-ENROLLMENT-01"

// ─── platform-symbol path constants (no bare literals) ────────────────────

const (
	policyRepoIfacePkg   = PlatformModulePath + "/corecells/accesscore/internal/ports"
	policyConformancePkg = PlatformModulePath + "/corecells/accesscore/internal/ports/conformance"
)

// ─── symbol name constants ─────────────────────────────────────────────────

const (
	policyRepoIfaceName   = "PolicyRepository"
	policyConformanceFunc = "RunPolicyRepoConformance"
)

// ─── detection helpers ─────────────────────────────────────────────────────

// collectPolicyRepoImpls adds to implSet all exported concrete types in pkg that
// implement PolicyRepository (directly or via pointer). Interface types are skipped.
// implPkgSet receives the package path for each collected impl.
func collectPolicyRepoImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() {
			continue
		}
		t := obj.Type()
		// Skip interface types — only concrete types can be registered probers.
		if _, isIface := t.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(t, iface) {
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// hasPolicyConformanceCall returns true when file contains at least one call to
// conformance.RunPolicyRepoConformance resolved via TypesInfo.
func hasPolicyConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && pkgPath == policyConformancePkg && name == policyConformanceFunc
	})
	return ok
}

// ─── internal step helpers ────────────────────────────────────────────────

// resolvePolicyRepoIfaceAndImpls loads the accesscore ports package together
// with all production packages (single packages.Load invocation), resolves
// the ports.PolicyRepository interface and collects all candidate impl packages.
// It returns (iface, implPkgs); iface is nil if resolution fails.
func resolvePolicyRepoIfaceAndImpls(t *testing.T, tags []string, root string) (*types.Interface, []*types.Package) {
	t.Helper()
	prodPatterns := prodscan.Patterns(root)
	// corecells is a separate go module, so prodscan.Patterns's root-relative
	// ./... does not cross the module boundary into it — the explicit
	// ./corecells/... is required to load the iface + impls in one packages.Load.
	ifacePatterns := append([]string{"./corecells/..."}, prodPatterns...)

	var policyRepoIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: tags}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == policyRepoIfacePkg {
				policyRepoIface = lookupPolicyRepoIface(p.Pkg)
				return nil
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	return policyRepoIface, implPkgs
}

// lookupPolicyRepoIface looks up and returns the PolicyRepository interface from
// the given package scope, or nil if it cannot be resolved.
func lookupPolicyRepoIface(pkg *types.Package) *types.Interface {
	obj := pkg.Scope().Lookup(policyRepoIfaceName)
	if obj == nil {
		return nil
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return nil
	}
	iface, ok := named.Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	return iface.Complete()
}

// scanPolicyConformanceEnrollments scans the test corpus and returns a set of
// canonical package paths that contain a conformance.RunPolicyRepoConformance
// call in a _test.go file.
func scanPolicyConformanceEnrollments(t *testing.T, tags []string, root string) map[string]bool {
	t.Helper()
	enrolledPkgs := make(map[string]bool)
	// corecells is a separate go module, so prodscan.Patterns's root-relative
	// ./... does not cross the module boundary into it — the explicit
	// ./corecells/... is required to load the iface + impls in one packages.Load.
	testPatterns := append([]string{"./corecells/..."}, prodscan.Patterns(root)...)
	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: tags}, testPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				if !strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				if hasPolicyConformanceCall(f, p.TypesInfo) {
					enrolledPkgs[canonicalPkgPath(p.Pkg.Path())] = true
				}
			}
			return nil
		})
	return enrolledPkgs
}

// flagUnenrolledPolicyImpls returns diagnostics for every impl key whose package
// does not appear in enrolledPkgs.
func flagUnenrolledPolicyImpls(implSet, enrolledPkgs map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for implKey := range implSet {
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		if enrolledPkgs[pkgPath] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: ports.PolicyRepository impl %q not enrolled in "+
					"conformance.RunPolicyRepoConformance test call "+
					"(POLICYREPO-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s that calls "+
					"conformance.RunPolicyRepoConformance(t, factory).",
				implKey, pkgPath,
			),
		})
	}
	return diags
}

// ─── CheckPolicyRepoConformanceEnrollment01 ────────────────────────────────

// CheckPolicyRepoConformanceEnrollment01 runs POLICYREPO-CONFORMANCE-ENROLLMENT-01
// over the running module and returns its diagnostics.
//
// This is the importable CellRule body. GoCell's own Test* functions in
// policy_repo_conformance_enrollment_test.go call the same detectors —
// single source, no parallel rule body.
//
// The rule is intentionally NOT registered in StandardCellRules: it reasons
// about GoCell's own internal package layout (accesscore ports/conformance),
// making it vacuous-green or false-red for an external module.
func CheckPolicyRepoConformanceEnrollment01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)

	// Step 1: resolve ports.PolicyRepository interface + collect impl packages.
	// iface and impls MUST share one packages.Load so types.Implements uses
	// pointer-identical *types.Named descriptors.
	policyRepoIface, implPkgs := resolvePolicyRepoIfaceAndImpls(t, cfg.BuildTags, root)

	if policyRepoIface == nil {
		return []Diagnostic{{
			Rel: policyRepoIfacePkg,
			Message: fmt.Sprintf(
				"POLICYREPO-CONFORMANCE-ENROLLMENT-01: failed to resolve ports.PolicyRepository interface; "+
					"check import path %s", policyRepoIfacePkg,
			),
		}}
	}

	// Step 2: collect all concrete implementations.
	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		collectPolicyRepoImpls(pkg, policyRepoIface, implSet, implPkgSet)
	}

	if len(implSet) == 0 {
		return []Diagnostic{{
			Rel: policyRepoIfacePkg,
			Message: "POLICYREPO-CONFORMANCE-ENROLLMENT-01: zero PolicyRepository implementations collected — " +
				"likely a type-universe regression (iface and impls must share one packages.Load). " +
				"Expect at least mem.PolicyRepository.",
		}}
	}

	// Step 3: scan test corpus for RunPolicyRepoConformance call sites.
	enrolledPkgs := scanPolicyConformanceEnrollments(t, cfg.BuildTags, root)

	// Step 4: flag unenrolled implementations.
	return flagUnenrolledPolicyImpls(implSet, enrolledPkgs)
}
