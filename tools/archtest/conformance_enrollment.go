package archtest

// conformance_enrollment.go — shared single-source detection logic for the
// *-CONFORMANCE-ENROLLMENT-* rule family (#2249).
//
// Six rules — repo-family (POLICYREPO / ROLEREPO / USERREPO over ports.*Repository)
// and saga-family (SAGA-JOURNAL / SAGA-GLOBALREADER / SAGA-OWNER-CHECKPOINT over
// kernel saga/projection interfaces) — historically each issued TWO packages.Load
// calls per run: a Tests=false load to resolve the interface + collect concrete
// production implementations, then a Tests=true load to scan the test corpus for
// conformance-suite enrollment. A Tests=true load is a structural superset of a
// Tests=false load, so the two collapse into ONE Tests=true load here, halving the
// per-rule wall-clock that the archtest-nightly slowgate budgets (#2249).
//
// Pointer-identity safety (the reason the prior code split the two loads):
// types.Implements requires the interface and the impl to reference
// pointer-identical *types.Named descriptors. Under Tests=true, go/packages
// surfaces a package WITH test files as its test-augmented variant (the Pass
// funnel dedups the regular variant away — see runRulePasses). All three target
// interface packages (accesscore/internal/ports, kernel/saga/journal,
// kernel/projection) carry NO _test.go, so each surfaces as its REGULAR variant;
// impls reference that same regular interface (no test-cycle splits a plain
// dependency), so types.Implements stays consistent. Impl collection is filtered
// to non-_test.go-declared types ([isTestDeclaredObj]) so a test double living in
// a test-augmented package (or an xtest package) is never miscounted as a
// production impl — preserving the prior Tests=false "production types only"
// semantics exactly.
//
// Non-test home (no build tag) mirrors the prior per-rule detector files so the
// detectors compile as part of package archtest; the rule bodies are GoCell-
// internal (none registered in StandardCellRules) and are driven only by GoCell's
// own Test* functions in *_conformance_enrollment_test.go / saga_invariants_test.go.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

// ─── repo-family rule IDs / symbol paths (consolidated from the deleted
//
//	*_repo_conformance_enrollment.go detector files) ────────────────────────
const (
	rulePolicyRepoConformanceEnrollment01 = "POLICYREPO-CONFORMANCE-ENROLLMENT-01"
	ruleRoleRepoConformanceEnrollment01   = "ROLEREPO-CONFORMANCE-ENROLLMENT-01"
	ruleUserRepoConformanceEnrollment01   = "USERREPO-CONFORMANCE-ENROLLMENT-01"

	// repoConformancePkg is the shared ports/conformance package: all three
	// repo conformance suites (RunPolicyRepoConformance / RunRoleRepoConformance /
	// RunUserRepoConformance) live here.
	repoPortsPkg       = PlatformCellsModulePath + "/accesscore/internal/ports"
	repoConformancePkg = PlatformCellsModulePath + "/accesscore/internal/ports/conformance"

	policyRepoIfaceName = "PolicyRepository"
	roleRepoIfaceName   = "RoleRepository"
	userRepoIfaceName   = "UserRepository"

	policyConformanceFunc = "RunPolicyRepoConformance"
	roleConformanceFunc   = "RunRoleRepoConformance"
	userConformanceFunc   = "RunUserRepoConformance"
)

// enrollPass captures, during the single Tests=true load, the per-package data the
// enrollment-credit phase needs — so credit runs AFTER implSet is fully resolved
// (the interface package may be visited after some impl packages).
type enrollPass struct {
	pkgPath   string
	info      *types.Info
	files     []*ast.File // all files in the pass (factory-body resolution needs siblings)
	testFiles []*ast.File // the _test.go subset (the conformance call sites)
}

// lookupNamedIface resolves the named interface ifaceName from pkg's scope, or nil.
func lookupNamedIface(pkg *types.Package, ifaceName string) *types.Interface {
	if pkg == nil {
		return nil
	}
	obj := pkg.Scope().Lookup(ifaceName)
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

// isCandidateImplType reports whether obj is a concrete (non-interface) named type
// eligible to be an implementation. exportedOnly restricts to exported types (the
// repo family; the saga family collects unexported impls too).
func isCandidateImplType(obj *types.TypeName, exportedOnly bool) bool {
	if exportedOnly && !obj.Exported() {
		return false
	}
	if _, isIface := obj.Type().Underlying().(*types.Interface); isIface {
		return false
	}
	return true
}

// canonicalPkgPath strips the "_test" or ".test" suffix from a test-variant
// package path to obtain the canonical production package path. Shared with the
// outbox subscriber/publisher enrollment rules.
func canonicalPkgPath(path string) string {
	path = strings.TrimSuffix(path, "_test")
	path = strings.TrimSuffix(path, ".test")
	return path
}

// isTestDeclaredObj reports whether obj is declared in a _test.go file. Used to
// keep impl collection to production types under the folded Tests=true load.
func isTestDeclaredObj(fset *token.FileSet, obj types.Object) bool {
	if fset == nil || obj == nil {
		return false
	}
	return strings.HasSuffix(fset.Position(obj.Pos()).Filename, "_test.go")
}

// collectImplsFromScope adds to implSet/implPkgSet every concrete named type in pkg
// that implements iface. exportedOnly restricts to exported types. It is the
// REDFixture-facing collector (operating on a Tests=false load's *types.Package, so
// no test-file filtering is required); the folded production path collects via
// [loadConformanceEnrollmentImpls] with a non-_test.go filter instead.
func collectImplsFromScope(
	pkg *types.Package, iface *types.Interface, exportedOnly bool, implSet, implPkgSet map[string]bool,
) {
	if pkg == nil || iface == nil {
		return
	}
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !isCandidateImplType(obj, exportedOnly) {
			continue
		}
		// typesutil.ImplementsInterface checks value-or-pointer receivers (the
		// sanctioned funnel for go/types.Implements; TYPESUTIL-IMPLEMENTS-FUNNEL-01).
		if typesutil.ImplementsInterface(obj.Type(), iface) {
			implSet[pkg.Path()+"."+name] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// hasConformanceCallTo reports whether file contains a call to pkgPath.funcName
// resolved via TypesInfo (the repo-family package-level enrollment predicate).
func hasConformanceCallTo(file *ast.File, info *types.Info, pkgPath, funcName string) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		p, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && p == pkgPath && name == funcName
	})
	return ok
}

// loadConformanceEnrollmentImpls performs the SINGLE Tests=true packages.Load that
// both resolves ifaceName from ifacePkg and collects its concrete production
// implementations, AND captures the test corpus for the credit phase. It folds the
// former Tests=false-iface + Tests=true-scan two-load design (#2249).
//
// collectFromIfacePkg selects whether types declared in the interface's own package
// are eligible impls (repo family excludes it; saga family includes it — preserving
// each family's prior behavior). loadPatterns must cover the interface package and
// the production tree; tags are the build tags for the load.
func loadConformanceEnrollmentImpls(
	t *testing.T, loadPatterns, tags []string,
	ifacePkg, ifaceName string, exportedOnly, collectFromIfacePkg bool,
) (iface *types.Interface, implSet map[string]bool, passes []enrollPass) {
	t.Helper()
	var candidates []implCandidate

	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: tags}, loadPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			isIfacePkg := p.Pkg.Path() == ifacePkg
			if iface == nil && isIfacePkg {
				iface = lookupNamedIface(p.Pkg, ifaceName)
			}
			if collectFromIfacePkg || !isIfacePkg {
				candidates = append(candidates, productionImplCandidates(p, exportedOnly)...)
			}
			if tf := testFilesOf(p); len(tf) > 0 {
				passes = append(passes, enrollPass{p.Pkg.Path(), p.TypesInfo, p.Files, tf})
			}
			return nil
		})

	return iface, implementersOf(candidates, iface), passes
}

// implementersOf returns the impl-key set ("pkg/path.TypeName") for every
// candidate that implements iface. Empty when iface is nil.
func implementersOf(candidates []implCandidate, iface *types.Interface) map[string]bool {
	implSet := map[string]bool{}
	if iface == nil {
		return implSet
	}
	for _, c := range candidates {
		// typesutil.ImplementsInterface is the sanctioned go/types.Implements
		// funnel (TYPESUTIL-IMPLEMENTS-FUNNEL-01); value-or-pointer receivers.
		if typesutil.ImplementsInterface(c.typ, iface) {
			implSet[c.pkgPath+"."+c.name] = true
		}
	}
	return implSet
}

// implCandidate is a non-_test.go-declared concrete named type collected during
// the single load, deferred for the post-load types.Implements filter.
type implCandidate struct {
	pkgPath string
	name    string
	typ     types.Type
}

// productionImplCandidates returns p's concrete named types declared in non-test
// files (production types only — the folded-load equivalent of the prior
// Tests=false collection; excludes in-package test types and xtest packages).
func productionImplCandidates(p *Pass, exportedOnly bool) []implCandidate {
	var out []implCandidate
	for _, name := range p.Pkg.Scope().Names() {
		obj, ok := p.Pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !isCandidateImplType(obj, exportedOnly) {
			continue
		}
		if isTestDeclaredObj(p.Fset, obj) {
			continue
		}
		out = append(out, implCandidate{p.Pkg.Path(), name, obj.Type()})
	}
	return out
}

// testFilesOf returns the _test.go files of p (the conformance call sites).
func testFilesOf(p *Pass) []*ast.File {
	var out []*ast.File
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

// flagUnenrolledByPkg returns a diagnostic for every impl whose package is not in
// enrolledPkgs (the repo family's package-level enrollment). msg renders the
// per-rule violation message from (implKey, pkgPath).
func flagUnenrolledByPkg(
	implSet, enrolledPkgs map[string]bool, msg func(implKey, pkgPath string) string,
) []Diagnostic {
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
		diags = append(diags, Diagnostic{Rel: implKey, Line: 0, Message: msg(implKey, pkgPath)})
	}
	return diags
}

// flagUnenrolledByImplKey returns a diagnostic for every impl not in enrolledImpls
// (the saga family's impl-level enrollment), sorted by Rel for stable output.
func flagUnenrolledByImplKey(
	implSet, enrolledImpls map[string]bool, msg func(implKey, pkgPath string) string,
) []Diagnostic {
	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		diags = append(diags, Diagnostic{Rel: implKey, Line: 0, Message: msg(implKey, pkgPath)})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}

// ─── repo family ────────────────────────────────────────────────────────────

// repoConformanceSpec parameterizes the three ports.*Repository enrollment rules.
type repoConformanceSpec struct {
	ruleID          string
	ifaceName       string
	conformanceFunc string
	humanIface      string // e.g. "ports.PolicyRepository" for messages
	emptyImplHint   string // e.g. "Expect at least mem.PolicyRepository."
	callSuffix      string // factory-arg shape shown in the remediation message
}

func policyRepoConformanceSpec() repoConformanceSpec {
	return repoConformanceSpec{
		ruleID: rulePolicyRepoConformanceEnrollment01, ifaceName: policyRepoIfaceName,
		conformanceFunc: policyConformanceFunc, humanIface: "ports.PolicyRepository",
		emptyImplHint: "Expect at least mem.PolicyRepository.", callSuffix: "(t, factory)",
	}
}

func roleRepoConformanceSpec() repoConformanceSpec {
	return repoConformanceSpec{
		ruleID: ruleRoleRepoConformanceEnrollment01, ifaceName: roleRepoIfaceName,
		conformanceFunc: roleConformanceFunc, humanIface: "ports.RoleRepository",
		emptyImplHint: "Expect at least mem.RoleRepository and postgres.PGRoleRepo.", callSuffix: "(t, factory)",
	}
}

func userRepoConformanceSpec() repoConformanceSpec {
	return repoConformanceSpec{
		ruleID: ruleUserRepoConformanceEnrollment01, ifaceName: userRepoIfaceName,
		conformanceFunc: userConformanceFunc, humanIface: "ports.UserRepository",
		emptyImplHint: "Expect at least mem.UserRepository and postgres.PGUserRepo.", callSuffix: "(t, factory, features)",
	}
}

// repoConformanceLoadPatterns is the iface∪scan pattern set for the repo family.
// corecells is a separate go module, so prodscan.Patterns's root-relative ./...
// does not cross into it — ./corecells/... is required to load the iface + impls.
func repoConformanceLoadPatterns(root string) []string {
	return append([]string{"./corecells/..."}, prodscan.Patterns(root)...)
}

// checkRepoConformanceEnrollment is the shared body for the three ports.*Repository
// enrollment rules. GoCell's own Test* functions call this; it is not registered in
// StandardCellRules (reasons about GoCell-internal accesscore layout).
func checkRepoConformanceEnrollment(t *testing.T, spec repoConformanceSpec, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	iface, implSet, passes := loadConformanceEnrollmentImpls(
		t, repoConformanceLoadPatterns(root), cfg.BuildTags,
		repoPortsPkg, spec.ifaceName, true /*exportedOnly*/, false /*collectFromIfacePkg*/)

	if iface == nil {
		return []Diagnostic{{Rel: repoPortsPkg, Message: fmt.Sprintf(
			"%s: failed to resolve %s interface; check import path %s",
			spec.ruleID, spec.humanIface, repoPortsPkg)}}
	}
	if len(implSet) == 0 {
		return []Diagnostic{{Rel: repoPortsPkg, Message: fmt.Sprintf(
			"%s: zero %s implementations collected — likely a type-universe regression "+
				"(iface and impls must share one packages.Load). %s",
			spec.ruleID, spec.ifaceName, spec.emptyImplHint)}}
	}

	enrolledPkgs := map[string]bool{}
	for _, pd := range passes {
		for _, f := range pd.testFiles {
			if hasConformanceCallTo(f, pd.info, repoConformancePkg, spec.conformanceFunc) {
				enrolledPkgs[canonicalPkgPath(pd.pkgPath)] = true
			}
		}
	}

	return flagUnenrolledByPkg(implSet, enrolledPkgs, func(implKey, pkgPath string) string {
		return fmt.Sprintf(
			"archtest: %s impl %q not enrolled in conformance.%s test call (%s). "+
				"Add a _test.go in package %s that calls conformance.%s%s.",
			spec.humanIface, implKey, spec.conformanceFunc, spec.ruleID,
			pkgPath, spec.conformanceFunc, spec.callSuffix)
	})
}

// ─── saga family ────────────────────────────────────────────────────────────

// sagaConformanceSpec parameterizes the three saga enrollment rules. credit scans
// one test file and marks the impls it enrolls (factory-closure form for journal /
// globalreader, direct-arg form for owner-checkpoint).
type sagaConformanceSpec struct {
	ruleID       string
	ifacePkg     string // interface package import path
	ifaceName    string
	humanIface   string // e.g. "kernel/saga/journal.Journal" for messages
	loadPatterns func(root string) []string
	credit       func(info *types.Info, files []*ast.File, file *ast.File, implSet, enrolledImpls map[string]bool)
	remediation  func(implKey, pkgPath string) string
}

// checkSagaConformanceEnrollment is the shared body for the three saga enrollment
// rules. Not registered in StandardCellRules (targets GoCell-internal impls).
func checkSagaConformanceEnrollment(t *testing.T, spec sagaConformanceSpec) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	iface, implSet, passes := loadConformanceEnrollmentImpls(
		t, spec.loadPatterns(root), FlatNonDefaultTags(),
		spec.ifacePkg, spec.ifaceName, false /*exportedOnly*/, true /*collectFromIfacePkg*/)

	if iface == nil {
		return []Diagnostic{{Rel: spec.ifacePkg, Message: fmt.Sprintf(
			"%s: failed to resolve %s interface; check import path %s",
			spec.ruleID, spec.humanIface, spec.ifacePkg)}}
	}

	enrolledImpls := map[string]bool{}
	for _, pd := range passes {
		for _, f := range pd.testFiles {
			spec.credit(pd.info, pd.files, f, implSet, enrolledImpls)
		}
	}

	return flagUnenrolledByImplKey(implSet, enrolledImpls, spec.remediation)
}

func sagaJournalConformanceSpec() sagaConformanceSpec {
	return sagaConformanceSpec{
		ruleID: "SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01", ifacePkg: sagaJournalPkg,
		ifaceName: sagaJournalIfaceName, humanIface: "kernel/saga/journal.Journal",
		loadPatterns: sagaJournalLoadPatterns,
		credit: func(info *types.Info, files []*ast.File, file *ast.File, implSet, enrolled map[string]bool) {
			creditEnrollmentsFromFactory(info, files, file, sagaConformanceFuncName, implSet, enrolled)
		},
		remediation: func(implKey, pkgPath string) string {
			return fmt.Sprintf(
				"archtest: kernel/saga/journal.Journal impl %q not enrolled "+
					"(SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01). Add a _test.go in package %s "+
					"(or its external _test) that both calls sagajournaltest.RunConformanceSuite(t, factory) "+
					"and constructs the impl inside the factory.", implKey, pkgPath)
		},
	}
}

func sagaGlobalReaderConformanceSpec() sagaConformanceSpec {
	return sagaConformanceSpec{
		ruleID: "SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01", ifacePkg: sagaJournalPkg,
		ifaceName: sagaGlobalReaderIfaceName, humanIface: "kernel/saga/journal.GlobalReader",
		loadPatterns: sagaJournalLoadPatterns,
		credit: func(info *types.Info, files []*ast.File, file *ast.File, implSet, enrolled map[string]bool) {
			creditEnrollmentsFromFactory(info, files, file, sagaGlobalReaderConformanceFunc, implSet, enrolled)
		},
		remediation: func(implKey, pkgPath string) string {
			return fmt.Sprintf(
				"archtest: kernel/saga/journal.GlobalReader impl %q not enrolled "+
					"(SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01). Add a _test.go in package %s "+
					"(or its external _test) that both calls sagajournaltest.RunGlobalReaderConformance(t, factory) "+
					"and constructs the impl inside the factory.", implKey, pkgPath)
		},
	}
}

func sagaOwnerCheckpointConformanceSpec() sagaConformanceSpec {
	return sagaConformanceSpec{
		ruleID: "SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01", ifacePkg: sagaKernelProjectionPkg,
		ifaceName: sagaOwnerCheckpointIfaceName, humanIface: "kernel/projection.OwnerCheckpointStore",
		loadPatterns: sagaOwnerCheckpointLoadPatterns,
		credit: func(info *types.Info, _ []*ast.File, file *ast.File, implSet, enrolled map[string]bool) {
			creditOwnerCheckpointEnrollments(info, file, sagaKernelProjectionTestPkg,
				sagaOwnerCheckpointConformanceFunc, implSet, enrolled)
		},
		remediation: func(implKey, pkgPath string) string {
			return fmt.Sprintf(
				"archtest: kernel/projection.OwnerCheckpointStore impl %q not enrolled "+
					"(SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01). Add a _test.go in package %s "+
					"(or its external _test) that calls projectiontest.RunOwnerCheckpointConformance(t, store) "+
					"with an instance of the impl.", implKey, pkgPath)
		},
	}
}

// sagaJournalLoadPatterns / sagaOwnerCheckpointLoadPatterns are the iface∪scan
// pattern sets for the saga family. The explicit interface-package prefix
// guarantees the interface package is loaded; it is a subset of prodscan's
// ./framework/kernel/... so scan coverage equals the prior prodscan-only scan.
func sagaJournalLoadPatterns(root string) []string {
	return append([]string{"./framework/kernel/saga/journal/..."}, prodscan.Patterns(root)...)
}

func sagaOwnerCheckpointLoadPatterns(root string) []string {
	return append([]string{"./framework/kernel/projection/..."}, prodscan.Patterns(root)...)
}
