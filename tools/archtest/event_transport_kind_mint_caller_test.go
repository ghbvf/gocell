//go:build archtest

// event_transport_kind_mint_caller_test.go — locks the SOLE sanctioned minter of
// the real-broker bootstrap.EventTransportKind (#2211, epic #1423).
//
// INVARIANT: EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01
//
// # What this guards
//
// bootstrap.RealBrokerEventTransport() mints the sealed EventTransportKind whose
// IsRealBroker()==true is the fact the phase0 broker-mandatory gate
// (validateSplitTopologyBroker) trusts to allow a split deployment topology. The
// gate replaced the older StorageBackend()=="postgres" ∧ non-nil proxy with this
// sealed fact ("non-nil ≠ real broker" closed). The PRIMARY seal is the type
// system: EventTransportKind has only unexported fields, so a package-external
// struct literal cannot forge a real-broker value (Hard, EVENT-TRANSPORT-KIND-
// SEALED-FIELD-FROZEN-01). The remaining escape is the exported constructor
// itself — any package importing bootstrap could call RealBrokerEventTransport()
// and hand the gate a "real broker" claim while wiring an in-process bus.
//
// This archtest pins every production call of RealBrokerEventTransport() to the
// sole sanctioned minter, cellmodules/eventtransport.Resolve, which mints it ONLY
// in the branch that constructs the RabbitMQ transport (resolveRabbitMQ). The
// composition roots thread the resolver's Transport.Kind into
// bootstrap.WithEventTransportKind — they never call the constructor themselves.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream (sealed construction): HARD — EventTransportKind's fields are
//     unexported, so a real-broker value cannot be struct-literal-forged
//     out-of-package (EVENT-TRANSPORT-KIND-SEALED-FIELD-FROZEN-01).
//   - Production end-to-end ("split ⇏ in-memory"): HARD via the sibling depguard
//     COREBUNDLE-EVENTBUS-FUNNEL-01 — an in-memory bus is import-unexpressible in
//     the production composition roots, so a forged real-broker kind cannot be
//     paired with one there.
//   - Upstream (only eventtransport.Resolve mints): MEDIUM — this caller-allowlist
//     archtest, a deliberately accepted permanent ceiling, not a deferred TODO.
//     EventTransportKind lives in bootstrap (framework module) so its gate can
//     consume it without an import cycle, but the sole minter is
//     cellmodules/eventtransport (root module); bootstrap must export the
//     constructor for it to call it, and a framework/.../internal/ package cannot
//     bridge a cross-module import, so Go visibility cannot express "only
//     eventtransport may mint a real-broker kind". Same Go/module ceiling
//     documented for the RowScopeAll minter, COMMAND-ASYNC-EMIT-CALLER-01,
//     OUTBOX-RECONSTRUCTION-CALLER-01 (#851/#893/#1282). No fake Hard-upgrade
//     issue is opened.
//
// # Detection is type-aware (not string scanning)
//
// ResolvePackageRef resolves call.Fun to runtime/bootstrap.RealBrokerEventTransport,
// alias- and dot-import-proof. No const-evaluation is needed (the callee identity
// alone is the trip).
//
// # Tool blind spots (charter §"强制盲区自检")
//
//  1. A real-broker kind laundered through a function value
//     (f := bootstrap.RealBrokerEventTransport; f()) is still a SelectorExpr the
//     ResolvePackageRef walk resolves, so it is caught. A kind returned by a
//     wrapper that itself calls the minter is caught at the wrapper's call.
//  2. _test.go files are out of scope (Production scope, Tests:false): test
//     helpers may mint a real-broker kind to exercise the gate. That is the same
//     posture as DEVICE-PRINCIPAL-MINT-CALLER-01's test exemption — a sealed value
//     minted in a test never reaches production wiring.
//  3. The RED fixture is build-tagged (archtest_fixture), excluded from the
//     default-tags Production scan, so it cannot pollute the production result.
package archtest

import (
	"fmt"
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
)

// eventTransportKindMinterName is the sealed real-broker minter on the bootstrap
// package (declared in event_transport_kind.go). bootstrapPkgPath is reused from
// probename_sealed_funnel.go (same archtest package).
const eventTransportKindMinterName = "RealBrokerEventTransport"

// eventTransportKindMinterPkgPath is the SOLE sanctioned caller package:
// cellmodules/eventtransport (root module), whose Resolve mints the real-broker
// kind only when it constructs the RabbitMQ transport.
const eventTransportKindMinterPkgPath = PlatformModulePath + "/cellmodules/eventtransport"

// eventTransportKindMintFixturePkg is the build-tagged RED fixture exercised by
// the reverse self-check.
const eventTransportKindMintFixturePkg = "./tools/archtest/internal/eventtransportkindmintfixture"

// isRealBrokerEventTransportCall reports whether call invokes
// bootstrap.RealBrokerEventTransport (alias/dot-import-proof via ResolvePackageRef).
func isRealBrokerEventTransportCall(p *Pass, call *ast.CallExpr) bool {
	pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	return ok && pkgPath == bootstrapPkgPath && name == eventTransportKindMinterName
}

// TestEventTransportKindMinterFunnel01 asserts every production call of
// bootstrap.RealBrokerEventTransport() sits in cellmodules/eventtransport, and
// that eventtransport actually mints it (anti-vacuity).
func TestEventTransportKindMinterFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var observed bool
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		inFunnel := p.Pkg.Path() == eventTransportKindMinterPkgPath
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if !isRealBrokerEventTransportCall(p, call) {
					return
				}
				observed = true
				if inFunnel {
					return // eventtransport.Resolve is the sanctioned minter
				}
				pos := p.Fset.Position(call.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01: %s calls bootstrap.RealBrokerEventTransport() "+
							"outside the sole sanctioned minter cellmodules/eventtransport.Resolve. A real-broker "+
							"EventTransportKind must be minted only where the RabbitMQ transport is actually "+
							"constructed (resolveRabbitMQ); minting it elsewhere hands the phase0 split-topology gate "+
							"(validateSplitTopologyBroker) a \"real broker\" claim that may not be backed by one. "+
							"Thread eventtransport.Resolve's Transport.Kind via bootstrap.WithEventTransportKind "+
							"instead; or, if this is a genuinely new sanctioned minter, widen the funnel allowlist "+
							"with a rationale.",
						rel),
				})
			})
		}
		return d
	})

	// Anti-vacuity: cellmodules/eventtransport must actually mint the real-broker
	// kind, else the funnel guards nothing (the minter was removed/renamed or the
	// scanner regressed).
	if !observed {
		diags = append(diags, Diagnostic{
			Message: "EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01 anti-vacuity: no production call to " +
				"bootstrap.RealBrokerEventTransport() was observed anywhere. eventtransport.Resolve " +
				"(resolveRabbitMQ) must mint it on the RabbitMQ branch — either the minter was " +
				"removed/renamed or the scanner regressed; the funnel guards nothing without it.",
		})
	}

	Report(t, "EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01", diags)
}

// TestEventTransportKindMinterFunnel01_RedFixture verifies the scanner fires
// against a package that mints the real-broker kind outside eventtransport, and
// does NOT flag the in-memory GREEN control. found==0 means the scanner is
// fail-open (anti-vacuity for the detector itself).
func TestEventTransportKindMinterFunnel01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{eventTransportKindMintFixturePkg}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		// The fixture package path is not cellmodules/eventtransport, so any
		// RealBrokerEventTransport call there is a violation; InMemoryEventTransport
		// (the GREEN control) must not be counted.
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if isRealBrokerEventTransportCall(p, call) {
					found++
				}
			})
		}
		return nil
	})

	assert.Equal(t, 1, found,
		"EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01 RED fixture self-check FAILED: expected exactly 1 "+
			"violation from eventtransportkindmintfixture (the ForgeRealBroker call; the SafeInMemory "+
			"InMemoryEventTransport control must NOT be flagged); got %d. found==0 means the scanner is "+
			"fail-open — check ResolvePackageRef resolves under the archtest_fixture tag.", found)
}
