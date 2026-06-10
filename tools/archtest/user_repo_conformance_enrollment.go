package archtest

// user_repo_conformance_enrollment.go — importable detection logic for
// USERREPO-CONFORMANCE-ENROLLMENT-01 (#1302 M3 Batch D).
//
// Non-test home of the detector so it can be compiled by an external
// Cell repository (Go never compiles a dependency's _test.go).
// GoCell's own Test* functions in user_repo_conformance_enrollment_test.go
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

const ruleUserRepoConformanceEnrollment01 = "USERREPO-CONFORMANCE-ENROLLMENT-01"

// ─── platform-symbol path constants (no bare literals) ────────────────────

const (
	userRepoIfacePkg = PlatformCellsModulePath + "/accesscore/internal/ports"
	conformancePkg   = PlatformCellsModulePath + "/accesscore/internal/ports/conformance"
)

// ─── symbol name constants ─────────────────────────────────────────────────

const (
	userRepoIfaceName = "UserRepository"
	conformanceFunc   = "RunUserRepoConformance"
)

// ─── detection helpers ─────────────────────────────────────────────────────

// collectUserRepoImpls adds to implSet all exported concrete types in pkg that
// implement UserRepository (directly or via pointer). Interface types are skipped.
// implPkgSet receives the package path for each collected impl.
func collectUserRepoImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
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

// hasConformanceCall returns true when file contains at least one call to
// conformance.RunUserRepoConformance resolved via TypesInfo.
func hasConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && pkgPath == conformancePkg && name == conformanceFunc
	})
	return ok
}

// canonicalPkgPath strips the "_test" or ".test" suffix from a test-variant
// package path to obtain the canonical production package path.
func canonicalPkgPath(path string) string {
	path = strings.TrimSuffix(path, "_test")
	path = strings.TrimSuffix(path, ".test")
	return path
}

// ─── internal step helpers ────────────────────────────────────────────────

// resolveUserRepoIfaceAndImpls loads the accesscore ports package together
// with all production packages (single packages.Load invocation), resolves
// the ports.UserRepository interface and collects all candidate impl packages.
// It returns (iface, implPkgs); iface is nil if resolution fails.
func resolveUserRepoIfaceAndImpls(t *testing.T, tags []string, root string) (*types.Interface, []*types.Package) {
	t.Helper()
	prodPatterns := prodscan.Patterns(root)
	// corecells is a separate go module, so prodscan.Patterns's root-relative
	// ./... does not cross the module boundary into it — the explicit
	// ./corecells/... is required to load the iface + impls in one packages.Load.
	ifacePatterns := append([]string{"./corecells/..."}, prodPatterns...)

	var userRepoIface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: tags}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == userRepoIfacePkg {
				userRepoIface = lookupUserRepoIface(p.Pkg)
				return nil
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	return userRepoIface, implPkgs
}

// lookupUserRepoIface looks up and returns the UserRepository interface from
// the given package scope, or nil if it cannot be resolved.
func lookupUserRepoIface(pkg *types.Package) *types.Interface {
	obj := pkg.Scope().Lookup(userRepoIfaceName)
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

// scanConformanceEnrollments scans the test corpus and returns a set of
// canonical package paths that contain a conformance.RunUserRepoConformance
// call in a _test.go file.
func scanConformanceEnrollments(t *testing.T, tags []string, root string) map[string]bool {
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
				if hasConformanceCall(f, p.TypesInfo) {
					enrolledPkgs[canonicalPkgPath(p.Pkg.Path())] = true
				}
			}
			return nil
		})
	return enrolledPkgs
}

// flagUnenrolledImpls returns diagnostics for every impl key whose package
// does not appear in enrolledPkgs.
func flagUnenrolledImpls(implSet, enrolledPkgs map[string]bool) []Diagnostic {
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
				"archtest: ports.UserRepository impl %q not enrolled in "+
					"conformance.RunUserRepoConformance test call "+
					"(USERREPO-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s that calls "+
					"conformance.RunUserRepoConformance(t, factory, features).",
				implKey, pkgPath,
			),
		})
	}
	return diags
}

// ─── CheckUserRepoConformanceEnrollment01 ────────────────────────────────

// CheckUserRepoConformanceEnrollment01 runs USERREPO-CONFORMANCE-ENROLLMENT-01
// over the running module and returns its diagnostics.
//
// This is the importable CellRule body. GoCell's own Test* functions in
// user_repo_conformance_enrollment_test.go call the same detectors —
// single source, no parallel rule body.
//
// The rule is intentionally NOT registered in StandardCellRules: it reasons
// about GoCell's own internal package layout (accesscore ports/conformance),
// making it vacuous-green or false-red for an external module.
func CheckUserRepoConformanceEnrollment01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)

	// Step 1: resolve ports.UserRepository interface + collect impl packages.
	// iface and impls MUST share one packages.Load so types.Implements uses
	// pointer-identical *types.Named descriptors.
	userRepoIface, implPkgs := resolveUserRepoIfaceAndImpls(t, cfg.BuildTags, root)

	if userRepoIface == nil {
		return []Diagnostic{{
			Rel: userRepoIfacePkg,
			Message: fmt.Sprintf(
				"USERREPO-CONFORMANCE-ENROLLMENT-01: failed to resolve ports.UserRepository interface; "+
					"check import path %s", userRepoIfacePkg,
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
		collectUserRepoImpls(pkg, userRepoIface, implSet, implPkgSet)
	}

	if len(implSet) == 0 {
		return []Diagnostic{{
			Rel: userRepoIfacePkg,
			Message: "USERREPO-CONFORMANCE-ENROLLMENT-01: zero UserRepository implementations collected — " +
				"likely a type-universe regression (iface and impls must share one packages.Load). " +
				"Expect at least mem.UserRepository and postgres.PGUserRepo.",
		}}
	}

	// Step 3: scan test corpus for RunUserRepoConformance call sites.
	enrolledPkgs := scanConformanceEnrollments(t, cfg.BuildTags, root)

	// Step 4: flag unenrolled implementations.
	return flagUnenrolledImpls(implSet, enrolledPkgs)
}
