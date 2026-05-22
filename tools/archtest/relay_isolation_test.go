// INVARIANT: RELAY-NOT-MANAGEDRESOURCE-01
//
// # RELAY-NOT-MANAGEDRESOURCE-01
//
// `*runtime/outbox.Relay` MUST NOT satisfy
// `kernel/lifecycle.ManagedResource`. The only sanctioned path that
// integrates a relay into Bootstrap's managed-resource lifecycle is
// `runtime/bootstrap.WithRelay` → `newRelayAdapter` (unexported, sole
// constructor). Any change that re-adds the lifecycle methods needed by
// ManagedResource (Checkers / Worker / Close) to `*Relay` would re-open
// the "WithManagedResource(relay) bypass" closed by ADR
// docs/architecture/202605201400-adr-relay-managedresource-isolation.md.
//
// AI-rebust grade: Hard (downstream). The type system itself rejects
// `WithManagedResource(*Relay)` at compile time; this archtest is the
// regression guard against re-satisfaction. Upstream Hard is the
// package-private `newRelayAdapter` constructor — external packages
// cannot express the adapter without going through `WithRelay`.
//
// Blind-spot inventory (tool: `types.Implements` + `*types.Named`):
//   - Method-set is determined via `types.NewPointer(named)` so we cover
//     both value- and pointer-receiver method declarations. The pointer
//     form is the one that satisfies ManagedResource today; the named
//     form covers a future receiver-set change.
//   - Embedding: if a struct embeds *Relay and re-exports nothing else,
//     the embedded *Relay would propagate any method set it gains. The
//     check on *Relay itself transitively catches the re-add — embedders
//     gain ManagedResource satisfaction only through the embedded type.
//   - Dot-import / aliased import: types live in `*types.Package`, not
//     under import aliases; resolution is alias-agnostic.
//   - Reverse self-check: the test also asserts the package-private
//     adapter `*runtime/bootstrap.relayAdapter` IS satisfied, proving the
//     filter is real (not vacuously passing).
package archtest

import (
	"go/types"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	relayIsoOutboxPkgPath    = "github.com/ghbvf/gocell/runtime/outbox"
	relayIsoLifecyclePkgPath = "github.com/ghbvf/gocell/kernel/lifecycle"
	relayIsoBootstrapPkgPath = "github.com/ghbvf/gocell/runtime/bootstrap"
	relayIsoRelayTypeName    = "Relay"
	relayIsoMRTypeName       = "ManagedResource"
	relayIsoAdapterTypeName  = "relayAdapter"
)

// TestRELAY_NOT_MANAGEDRESOURCE_01 fails if *runtime/outbox.Relay satisfies
// kernel/lifecycle.ManagedResource, OR if *runtime/bootstrap.relayAdapter
// does not (the positive control — proves the filter is exercising real
// types from the production type universe).
func TestRELAY_NOT_MANAGEDRESOURCE_01(t *testing.T) {
	t.Parallel()

	var relayNamed *types.Named
	var managedResourceIface *types.Interface
	var adapterNamed *types.Named

	loadPatterns := []string{
		"./runtime/outbox/...",
		"./kernel/lifecycle/...",
		"./runtime/bootstrap/...",
	}

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, loadPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			switch p.Pkg.Path() {
			case relayIsoOutboxPkgPath:
				if obj := p.Pkg.Scope().Lookup(relayIsoRelayTypeName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						relayNamed = named
					}
				}
			case relayIsoLifecyclePkgPath:
				if obj := p.Pkg.Scope().Lookup(relayIsoMRTypeName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							managedResourceIface = iface.Complete()
						}
					}
				}
			case relayIsoBootstrapPkgPath:
				if obj := p.Pkg.Scope().Lookup(relayIsoAdapterTypeName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						adapterNamed = named
					}
				}
			}
			return nil
		})

	require.NotNil(t, relayNamed,
		"RELAY-NOT-MANAGEDRESOURCE-01: failed to resolve %s.%s — type-universe regression",
		relayIsoOutboxPkgPath, relayIsoRelayTypeName)
	require.NotNil(t, managedResourceIface,
		"RELAY-NOT-MANAGEDRESOURCE-01: failed to resolve %s.%s — type-universe regression",
		relayIsoLifecyclePkgPath, relayIsoMRTypeName)
	require.NotNil(t, adapterNamed,
		"RELAY-NOT-MANAGEDRESOURCE-01: failed to resolve %s.%s — type-universe regression",
		relayIsoBootstrapPkgPath, relayIsoAdapterTypeName)

	// Core invariant: *Relay must NOT satisfy ManagedResource. Use the
	// value-or-pointer funnel so both pointer- and value-receiver method
	// sets are considered — re-adding Close(ctx) error in either form would
	// regress the type isolation and must be caught here.
	if typesutil.ImplementsInterface(relayNamed, managedResourceIface) {
		t.Fatalf(
			"RELAY-NOT-MANAGEDRESOURCE-01: %s.%s now satisfies %s.%s — the type "+
				"isolation introduced by ADR 202605201400 has regressed. "+
				"Likely cause: a method named Close(ctx context.Context) error was "+
				"added (or restored) on *Relay. Remove the method and let the "+
				"package-private relayAdapter satisfy ManagedResource on its behalf.",
			relayIsoOutboxPkgPath, relayIsoRelayTypeName,
			relayIsoLifecyclePkgPath, relayIsoMRTypeName,
		)
	}

	// Positive reverse self-check: the sanctioned adapter must satisfy the
	// interface; otherwise the filter is vacuous. *relayAdapter is the
	// sole sanctioned holder under the §"single sanctioned holder" Hard
	// 范本 (.claude/rules/gocell/ai-collab.md). The pointer wrap matches the
	// receiver set of the adapter's Checkers/Worker/Close methods.
	require.True(t, typesutil.ImplementsInterface(types.NewPointer(adapterNamed), managedResourceIface),
		"RELAY-NOT-MANAGEDRESOURCE-01 self-check: *%s.%s no longer satisfies "+
			"%s.%s — the bootstrap relay funnel is broken; review newRelayAdapter "+
			"and its Checkers/Worker/Close methods.",
		relayIsoBootstrapPkgPath, relayIsoAdapterTypeName,
		relayIsoLifecyclePkgPath, relayIsoMRTypeName)
}
