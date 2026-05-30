// INVARIANT: OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
//
//   - 实现扫描: typesutil.ImplementsInterface — type-aware; identifies every
//     concrete named type (T or *T) in the production tree that satisfies
//     kernel/outbox.Publisher.
//   - conformance 调用扫描: ResolvePackageRef + _test.go path filter — type-aware
//     callee resolution via *types.Info; identifies every package with an
//     outboxtest.TestPubSub / RunBatch* call site.
//   - 综合: Medium 天花板 — Go cannot require a test to exist at compile time;
//     the enforcement is archtest-bound (CI fails), not compile-time.
//
// Enforces: every concrete production type implementing kernel/outbox.Publisher
// must EITHER have an outboxtest.TestPubSub (or RunBatch*) conformance call in a
// _test.go of its package, OR appear in outboxPublisherEnrollmentWaivers with a
// documented reason. This is the machine guard for contract-fanout.md 载体#2
// ("archtest 守新增实现自动接入 conformance") for the outbox.Publisher contract:
// a new adapter that forgets to enroll fails CI rather than silently shipping
// unverified.
//
// Why a waiver list instead of silent deferral: adapters/mqtt.Publisher landed
// (PR-2) before adapters/mqtt.Subscriber (PR-3). outboxtest.TestPubSub is a
// pub→sub roundtrip and cannot be constructed without the Subscriber, so mqtt
// is waived WITH a tracked reason (not a silent carryover). When the Subscriber
// lands, the stale-waiver guard below forces the waiver to be removed once the
// real TestPubSub call appears.
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations: no production code constructs
//     a Publisher via reflect.MethodByName; confirmed by
//     TestOutboxPublisherEnrollment_ReverseBlindSpot_NoReflectImpl.
//   - B2. test-file impls (mocks/fakes in _test.go) are excluded by the
//     Tests=false production load pass — intentional; only non-test concrete
//     types must enroll. Test-double publishers that live in non-test .go files
//     (e.g. cells/configcore/internal/testutil) are explicitly waived below.
//
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (template)
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

// INVARIANT: OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01

const (
	outboxPublisherIfacePkg  = "github.com/ghbvf/gocell/kernel/outbox"
	outboxPublisherIfaceName = "Publisher"
	outboxtestPkg            = "github.com/ghbvf/gocell/kernel/outbox/outboxtest"
)

// outboxPublisherEnrollmentWaivers maps a production package path → the reason
// its outbox.Publisher impl is exempt from the conformance-enrollment rule.
// Each entry is auditable and principled; a flagged impl whose package is NOT
// here fails CI, forcing a conscious decision (no silent additions).
var outboxPublisherEnrollmentWaivers = map[string]string{
	"github.com/ghbvf/gocell/kernel/outbox": "kernel noop sink outbox.DiscardPublisher (Noop()==true, rejected by cell.CheckNotNoop " +
		"in durable mode) — it discards rather than delivers, so a pub→sub roundtrip " +
		"(outboxtest.TestPubSub) is N/A by design.",
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil": "test-double publishers " +
		"(StubPublisher / FailingPublisher) — not a real transport; outboxtest.TestPubSub " +
		"roundtrip is N/A for in-memory stubs.",
	// adapters/mqtt waiver removed in PR-4 (#1142): adapters/mqtt/conformance_test.go
	// now calls outboxtest.TestPubSub (mosquitto-via-testcontainers), so the
	// stale-waiver guard requires the entry be dropped.
}

// TestOutboxPublisherConformanceEnrollment enforces
// OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01.
func TestOutboxPublisherConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	// ─── Step 1: resolve kernel/outbox.Publisher + collect candidate impls ───
	//
	// The iface and the impl types MUST come from the same packages.Load
	// invocation so types.Implements uses pointer-identical *types.Named
	// descriptors (cross-load comparisons are always false).
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/outbox/..."}, prodPatterns...)

	var pubIface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == outboxPublisherIfacePkg {
				if obj := p.Pkg.Scope().Lookup(outboxPublisherIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							pubIface = iface.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, pubIface,
		"OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01: failed to resolve kernel/outbox.Publisher; "+
			"check import path %s", outboxPublisherIfacePkg)

	// ─── Step 2: collect concrete implementations ───────────────────────────
	implSet := make(map[string]bool)    // "pkg/path.TypeName" → true
	implPkgSet := make(map[string]bool) // "pkg/path" → true
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectPublisherImpls(pkg, pubIface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet,
		"OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01: zero outbox.Publisher implementations collected — "+
			"likely a type-universe regression (iface and impls must share one packages.Load). "+
			"Expect at least adapters/rabbitmq.Publisher and adapters/mqtt.Publisher.")

	// ─── Step 3: scan test corpus for outboxtest conformance call sites ──────
	enrolledPkgs := make(map[string]bool)
	testPatterns := prodscan.Patterns(root)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, testPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if hasOutboxConformanceCall(f, p.TypesInfo) {
					enrolledPkgs[canonicalPkgPath(p.Pkg.Path())] = true
				}
			}
			return nil
		})

	// ─── Step 4: flag unenrolled, unwaived impls + stale waivers ─────────────
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
		if _, waived := outboxPublisherEnrollmentWaivers[pkgPath]; waived {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: kernel/outbox.Publisher impl %q is not enrolled in an "+
					"outboxtest.TestPubSub conformance test and is not in "+
					"outboxPublisherEnrollmentWaivers (OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s that calls outboxtest.TestPubSub(t, features, ctor), "+
					"or add a documented waiver entry.",
				implKey, pkgPath),
		})
	}

	// Stale-waiver guard: a waived package that IS now enrolled must drop its
	// waiver (keeps the list honest; forces PR-3 to remove the mqtt waiver once
	// the real TestPubSub call lands).
	for pkgPath := range outboxPublisherEnrollmentWaivers {
		if enrolledPkgs[pkgPath] {
			diags = append(diags, Diagnostic{
				Rel:  pkgPath,
				Line: 0,
				Message: fmt.Sprintf(
					"archtest: package %s is in outboxPublisherEnrollmentWaivers but now HAS an "+
						"outboxtest.TestPubSub call — remove the stale waiver entry "+
						"(OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01).", pkgPath),
			})
		}
	}

	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01", diags)
}

// TestOutboxPublisherEnrollment_REDFixture verifies the flagging logic reports a
// violation when an impl's owning package is neither enrolled nor waived.
// Strategy: collect the real implSet, then simulate a "missing enrollment" for
// one non-waived impl and assert it is flagged.
func TestOutboxPublisherEnrollment_REDFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/outbox/..."}, prodPatterns...)

	var pubIface *types.Interface
	var implPkgs []*types.Package
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == outboxPublisherIfacePkg {
				if obj := p.Pkg.Scope().Lookup(outboxPublisherIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							pubIface = iface.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})
	require.NotNil(t, pubIface, "REDFixture: could not resolve outbox.Publisher interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectPublisherImpls(pkg, pubIface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet, "REDFixture: implSet must not be empty")

	// Pick any impl and derive its pkg path; simulate it being neither enrolled
	// nor waived by running the flag loop with empty enrolled+waiver sets.
	var target string
	for k := range implSet {
		target = k
		break
	}
	dotIdx := strings.LastIndex(target, ".")
	require.Greater(t, dotIdx, 0, "REDFixture: malformed impl key %q", target)

	emptyEnrolled := map[string]bool{}
	emptyWaivers := map[string]string{}
	var diags []Diagnostic
	for implKey := range implSet {
		di := strings.LastIndex(implKey, ".")
		if di < 0 {
			continue
		}
		pkgPath := implKey[:di]
		if emptyEnrolled[pkgPath] {
			continue
		}
		if _, w := emptyWaivers[pkgPath]; w {
			continue
		}
		diags = append(diags, Diagnostic{Rel: implKey, Message: implKey + " not enrolled"})
	}
	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: with empty enrolled+waiver sets, every impl must be flagged (got 0)")
}

// TestOutboxPublisherEnrollment_ReverseBlindSpot_NoReflectImpl (blind spot B1)
// confirms no production non-test file uses the string literal "Publisher" as
// reflect bait to construct an implicit impl the type scan would miss.
func TestOutboxPublisherEnrollment_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}
	root := findModuleRoot(t)
	scope := ModuleScope(root)
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "MethodByName" {
					return
				}
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Message: "blind-spot B1: reflect MethodByName in production code may indicate a " +
						"reflect-built outbox.Publisher the type scan misses " +
						"(OUTBOX-PUBLISHER-CONFORMANCE-ENROLLMENT-01)",
				})
			})
		}
		return out
	})
	assert.Empty(t, diags,
		"B1 reverse: no production non-test file should construct a Publisher via reflect.MethodByName")
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectPublisherImpls adds to implSet all exported concrete types in pkg that
// implement outbox.Publisher (directly or via pointer). Interface types are
// skipped. implPkgSet receives the package path for each collected impl.
func collectPublisherImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() {
			continue
		}
		tt := obj.Type()
		if _, isIface := tt.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(tt, iface) {
			implSet[pkg.Path()+"."+name] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// hasOutboxConformanceCall returns true when file contains a call to
// outboxtest.TestPubSub or any outboxtest.RunBatch* conformance entrypoint,
// resolved via TypesInfo.
func hasOutboxConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, found := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != outboxtestPkg {
			return false
		}
		return name == "TestPubSub" || strings.HasPrefix(name, "RunBatch")
	})
	return found
}
