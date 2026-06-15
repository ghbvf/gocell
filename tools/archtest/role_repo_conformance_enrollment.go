package archtest

// role_repo_conformance_enrollment.go — importable detection logic for
// ROLEREPO-CONFORMANCE-ENROLLMENT-01 (#1709).
//
// Sibling of user_repo_conformance_enrollment.go / policy_repo_conformance_enrollment.go:
// #1709 introduced a shared RowScope obligation matrix into RunRoleRepoConformance,
// so — exactly as for UserRepository — every concrete RoleRepository impl must be
// pinned to that suite, else a future backend can silently skip the row-visibility
// conformance cases (the body-ignores-vis defense is only as strong as its coverage).
//
// Non-test home of the detector so it can be compiled by an external
// Cell repository (Go never compiles a dependency's _test.go).
// GoCell's own Test* functions in role_repo_conformance_enrollment_test.go
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

const ruleRoleRepoConformanceEnrollment01 = "ROLEREPO-CONFORMANCE-ENROLLMENT-01"

// ─── platform-symbol path constants (no bare literals) ────────────────────

const (
	roleRepoIfacePkg   = PlatformCellsModulePath + "/accesscore/internal/ports"
	roleConformancePkg = PlatformCellsModulePath + "/accesscore/internal/ports/conformance"
)

// ─── symbol name constants ─────────────────────────────────────────────────

const (
	roleRepoIfaceName   = "RoleRepository"
	roleConformanceFunc = "RunRoleRepoConformance"
)

// ─── detection helpers ─────────────────────────────────────────────────────

// collectRoleRepoImpls adds to implSet all exported concrete types in pkg that
// implement RoleRepository (directly or via pointer). Interface types are skipped.
// implPkgSet receives the package path for each collected impl.
func collectRoleRepoImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
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

// hasRoleConformanceCall returns true when file contains at least one call to
// conformance.RunRoleRepoConformance resolved via TypesInfo.
func hasRoleConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && pkgPath == roleConformancePkg && name == roleConformanceFunc
	})
	return ok
}

// ─── internal step helpers ────────────────────────────────────────────────

// resolveRoleRepoIfaceAndImpls loads the accesscore ports package together
// with all production packages (single packages.Load invocation), resolves
// the ports.RoleRepository interface and collects all candidate impl packages.
// It returns (iface, implPkgs); iface is nil if resolution fails.
func resolveRoleRepoIfaceAndImpls(t *testing.T, tags []string, root string) (*types.Interface, []*types.Package) {
	t.Helper()
	prodPatterns := prodscan.Patterns(root)
	// corecells is a separate go module, so prodscan.Patterns's root-relative
	// ./... does not cross the module boundary into it — the explicit
	// ./corecells/... is required to load the iface + impls in one packages.Load.
	ifacePatterns := append([]string{"./corecells/..."}, prodPatterns...)

	var roleRepoIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: tags}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == roleRepoIfacePkg {
				roleRepoIface = lookupRoleRepoIface(p.Pkg)
				return nil
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	return roleRepoIface, implPkgs
}

// lookupRoleRepoIface looks up and returns the RoleRepository interface from
// the given package scope, or nil if it cannot be resolved.
func lookupRoleRepoIface(pkg *types.Package) *types.Interface {
	obj := pkg.Scope().Lookup(roleRepoIfaceName)
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

// scanRoleConformanceEnrollments scans the test corpus and returns a set of
// canonical package paths that contain a conformance.RunRoleRepoConformance
// call in a _test.go file.
func scanRoleConformanceEnrollments(t *testing.T, tags []string, root string) map[string]bool {
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
				if hasRoleConformanceCall(f, p.TypesInfo) {
					enrolledPkgs[canonicalPkgPath(p.Pkg.Path())] = true
				}
			}
			return nil
		})
	return enrolledPkgs
}

// flagUnenrolledRoleImpls returns diagnostics for every impl key whose package
// does not appear in enrolledPkgs.
func flagUnenrolledRoleImpls(implSet, enrolledPkgs map[string]bool) []Diagnostic {
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
				"archtest: ports.RoleRepository impl %q not enrolled in "+
					"conformance.RunRoleRepoConformance test call "+
					"(ROLEREPO-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s that calls "+
					"conformance.RunRoleRepoConformance(t, factory).",
				implKey, pkgPath,
			),
		})
	}
	return diags
}

// ─── CheckRoleRepoConformanceEnrollment01 ────────────────────────────────

// CheckRoleRepoConformanceEnrollment01 runs ROLEREPO-CONFORMANCE-ENROLLMENT-01
// over the running module and returns its diagnostics.
//
// This is the importable CellRule body. GoCell's own Test* functions in
// role_repo_conformance_enrollment_test.go call the same detectors —
// single source, no parallel rule body.
//
// The rule is intentionally NOT registered in StandardCellRules: it reasons
// about GoCell's own internal package layout (accesscore ports/conformance),
// making it vacuous-green or false-red for an external module.
func CheckRoleRepoConformanceEnrollment01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)

	// Step 1: resolve ports.RoleRepository interface + collect impl packages.
	// iface and impls MUST share one packages.Load so types.Implements uses
	// pointer-identical *types.Named descriptors.
	roleRepoIface, implPkgs := resolveRoleRepoIfaceAndImpls(t, cfg.BuildTags, root)

	if roleRepoIface == nil {
		return []Diagnostic{{
			Rel: roleRepoIfacePkg,
			Message: fmt.Sprintf(
				"ROLEREPO-CONFORMANCE-ENROLLMENT-01: failed to resolve ports.RoleRepository interface; "+
					"check import path %s", roleRepoIfacePkg,
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
		collectRoleRepoImpls(pkg, roleRepoIface, implSet, implPkgSet)
	}

	if len(implSet) == 0 {
		return []Diagnostic{{
			Rel: roleRepoIfacePkg,
			Message: "ROLEREPO-CONFORMANCE-ENROLLMENT-01: zero RoleRepository implementations collected — " +
				"likely a type-universe regression (iface and impls must share one packages.Load). " +
				"Expect at least mem.RoleRepository and postgres.PGRoleRepo.",
		}}
	}

	// Step 3: scan test corpus for RunRoleRepoConformance call sites.
	enrolledPkgs := scanRoleConformanceEnrollments(t, cfg.BuildTags, root)

	// Step 4: flag unenrolled implementations.
	return flagUnenrolledRoleImpls(implSet, enrolledPkgs)
}
