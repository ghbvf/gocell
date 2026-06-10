//go:build archtest

// INVARIANT: OUTBOX-SUBSCRIBER-SETTLEMENT-NOTIFY-01
//
// Every terminal outbox.Subscriber implementation must call
// outbox.NotifySettlement on its broker-settlement path, so SettlementObservers
// attached to a HandleResult (by kernel/wrapper, observability middleware, …)
// fire after the message is settled. This is the F4 regression guard: the MQTT
// Subscriber originally never called NotifySettlement (rabbitmq + the in-mem
// EventBus did), silently dropping observers on the MQTT consume path.
//
// The rule: for each EXPORTED concrete type that implements kernel/outbox.Subscriber
// in production code, its defining package must contain at least one call to
// outbox.NotifySettlement.
//
// Scope / blind spots (charter §盲区自检):
//   - Exported terminal impls only. collectPublisherImpls filters to exported
//     types, which naturally excludes the unexported delegating decorator
//     runtime/eventrouter.contractTracingSubscriber — a decorator forwards
//     Subscribe to an inner Subscriber and MUST NOT settle itself, so requiring
//     a NotifySettlement call there would be wrong. A future EXPORTED non-terminal
//     (delegating) Subscriber would need an entry in
//     outboxSubscriberSettlementWaivers with a justification.
//   - Call-site form. The detector resolves the callee via go/types
//     (ResolvePackageRef), so it matches outbox.NotifySettlement and a
//     dot-imported NotifySettlement, but NOT a method-value / reflect indirection
//     (`f := outbox.NotifySettlement; f(…)`). That form is not used in this repo;
//     TestOutboxSubscriberSettlementNotify_DetectorFires guards against the
//     resolution path silently breaking (which would make the rule vacuous).
//   - Package granularity (Medium, not Hard). A call anywhere in the impl's
//     package satisfies the rule; pairing the per-adapter white-box behavior test
//     (e.g. TestSubscriber_NotifySettlement_FiresObservers) proves the call is on
//     the live dispatch path. A Hard form would require the broker-backed
//     outboxtest.TestPubSub conformance to assert observer firing across all
//     impls — that lands with TestPubSub in PR-4 (see the adapters/mqtt waiver in
//     outbox_publisher_conformance_enrollment_test.go).

package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const (
	outboxSubscriberIfaceName  = "Subscriber"
	outboxNotifySettlementName = "NotifySettlement"
)

// outboxSubscriberSettlementWaivers maps a production package path → the reason
// its exported outbox.Subscriber impl is exempt from the NotifySettlement rule.
// Empty today (all three terminal impls — eventbus / rabbitmq / mqtt — settle
// observers). A delegating decorator that is exported would belong here.
var outboxSubscriberSettlementWaivers = map[string]string{}

// scanOutboxSubscriberSettlement resolves kernel/outbox.Subscriber, collects the
// exported impls + their packages, and records which packages call
// outbox.NotifySettlement — all from one packages.Load so the type universe is
// shared.
func scanOutboxSubscriberSettlement(t *testing.T) (implPkgSet, notifyPkgSet map[string]bool) {
	t.Helper()
	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/outbox/..."}, prodPatterns...)

	var subIface *types.Interface
	var allPkgs []*types.Package
	notifyPkgSet = make(map[string]bool)

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == outboxPublisherIfacePkg {
				if obj := p.Pkg.Scope().Lookup(outboxSubscriberIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							subIface = iface.Complete()
						}
					}
				}
			}
			allPkgs = append(allPkgs, p.Pkg)
			if p.TypesInfo != nil {
				for _, f := range p.Files {
					if strings.HasSuffix(p.Rel(f), "_test.go") {
						continue
					}
					EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
						pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
						if ok && pkgPath == outboxPublisherIfacePkg && name == outboxNotifySettlementName {
							notifyPkgSet[canonicalPkgPath(p.Pkg.Path())] = true
						}
					})
				}
			}
			return nil
		})

	require.NotNil(t, subIface,
		"OUTBOX-SUBSCRIBER-SETTLEMENT-NOTIFY-01: failed to resolve kernel/outbox.Subscriber")

	implPkgSet = make(map[string]bool)
	implSet := make(map[string]bool)
	for _, pkg := range allPkgs {
		if pkg != nil {
			collectPublisherImpls(pkg, subIface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet,
		"OUTBOX-SUBSCRIBER-SETTLEMENT-NOTIFY-01: zero outbox.Subscriber implementations collected — "+
			"likely a type-universe regression. Expect adapters/rabbitmq.Subscriber, "+
			"adapters/mqtt.Subscriber, runtime/eventbus.InMemoryEventBus.")
	return implPkgSet, notifyPkgSet
}

// TestOutboxSubscriberSettlementNotify enforces OUTBOX-SUBSCRIBER-SETTLEMENT-NOTIFY-01:
// every exported outbox.Subscriber impl's package calls outbox.NotifySettlement.
func TestOutboxSubscriberSettlementNotify(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	implPkgSet, notifyPkgSet := scanOutboxSubscriberSettlement(t)

	var diags []Diagnostic
	for pkgPath := range implPkgSet {
		canon := canonicalPkgPath(pkgPath)
		if notifyPkgSet[canon] {
			continue
		}
		if _, waived := outboxSubscriberSettlementWaivers[canon]; waived {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  pkgPath,
			Line: 0,
			Message: fmt.Sprintf(
				"OUTBOX-SUBSCRIBER-SETTLEMENT-NOTIFY-01: package %s has an exported outbox.Subscriber "+
					"implementation but never calls outbox.NotifySettlement — SettlementObservers attached "+
					"to a HandleResult would be silently dropped on its consume path. Call "+
					"outbox.NotifySettlement on every broker-settlement branch (mirror adapters/rabbitmq), "+
					"or add a documented waiver if this is a delegating decorator.", pkgPath,
			),
		})
	}

	// Stale-waiver guard: a waived package that now DOES call NotifySettlement must
	// drop its waiver (keeps the list honest).
	for pkgPath := range outboxSubscriberSettlementWaivers {
		if notifyPkgSet[canonicalPkgPath(pkgPath)] {
			diags = append(diags, Diagnostic{
				Rel:  pkgPath,
				Line: 0,
				Message: fmt.Sprintf(
					"OUTBOX-SUBSCRIBER-SETTLEMENT-NOTIFY-01: package %s is waived but now calls "+
						"outbox.NotifySettlement — remove the stale waiver entry.", pkgPath,
				),
			})
		}
	}

	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "OUTBOX-SUBSCRIBER-SETTLEMENT-NOTIFY-01", diags)
}

// TestOutboxSubscriberSettlementNotify_DetectorFires proves the
// outbox.NotifySettlement callee-resolution path is live: the scan must find the
// call in at least the known terminal-impl packages. If this drops to zero the
// main test would pass vacuously (no calls found ⇒ but also impls would be
// flagged — this guard catches the "resolution silently broke AND impls were
// mis-collected" double failure mode directly on the detector).
func TestOutboxSubscriberSettlementNotify_DetectorFires(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	_, notifyPkgSet := scanOutboxSubscriberSettlement(t)
	require.GreaterOrEqual(t, len(notifyPkgSet), 3,
		"OUTBOX-SUBSCRIBER-SETTLEMENT-NOTIFY-01: expected outbox.NotifySettlement calls in at least the "+
			"three terminal Subscriber packages (rabbitmq / mqtt / eventbus); found %d — the "+
			"ResolvePackageRef resolution path may be silently broken", len(notifyPkgSet))
}
