// invariants:
//   - INVARIANT: RELAY-NOT-MANAGEDRESOURCE-01
//   - INVARIANT: RELAY-SOLE-HOLDER-01
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
//
// # RELAY-SOLE-HOLDER-01
//
// Among all production named struct types satisfying
// `kernel/lifecycle.ManagedResource`, the ONLY type permitted to hold a
// `*runtime/outbox.Relay` field (named, embedded, or by value) is
// `runtime/bootstrap.relayAdapter`. Any other satisfying-and-holding type
// would constitute an alternative sanctioned-holder path, breaking the
// §"single sanctioned holder" Hard 范本
// (.claude/rules/gocell/ai-collab.md) even when the upstream
// `newRelayAdapter` constructor stays package-private.
//
// AI-rebust grade: Hard (downstream). The production type universe is
// walked via `RunTypedProduction`; for each `*types.Named` whose
// underlying is `*types.Struct` and whose pointer method set satisfies
// `ManagedResource`, every struct field is inspected and any
// `*Relay` / `Relay` field that does not live in `relayAdapter` fails the
// test. Upstream Hard is again the package-private `newRelayAdapter`
// constructor — external packages cannot construct an alternative
// wrapper that holds the relay.
//
// Blind-spot inventory (tool: `types.Implements` + `*types.Struct`
// field walk):
//   - Generic origin types (`TypeParams() != nil`) are skipped: the
//     rule applies to instantiated / non-generic types. A generic
//     holder cannot satisfy ManagedResource without being instantiated;
//     each instantiation surfaces as a concrete `*types.Named` that the
//     walk catches.
//   - Embedded *Relay is the same shape as a named pointer field in
//     go/types (`Field(i).Anonymous() == true`, same Type()); the field
//     walk does not need to special-case embedding.
//   - Indirect-via-interface (e.g. a struct holds
//     `interface{ Worker(); Close(...)... }`) is NOT inspected by this
//     rule — but reaching such a wrapper to a real `*Relay` still
//     requires going through `WithRelay` (the sole sanctioned entry),
//     which already routes through `relayAdapter`. New public Bootstrap
//     options that accept a `*Relay` from outside the package would be
//     caught by inspection of their resulting struct fields.
//   - Generated/ packages are excluded by `RunTypedProduction`
//     (production loader filters `<module>/generated/`).
//   - Reverse self-check: `runtime/bootstrap.relayAdapter` MUST appear
//     in the satisfying-AND-holding set, otherwise the filter is
//     vacuous and the test passes silently.

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

// fieldHoldsRelay reports whether t resolves to *relayNamed (pointer field
// or embedded *Relay) or relayNamed (value field / embedded Relay).
// Type identity is checked via *types.Named.Obj() rather than
// types.Identical so the comparison is robust against repeated calls to
// types.NewNamed and instantiation.
func fieldHoldsRelay(ft types.Type, relayNamed *types.Named) bool {
	switch ty := ft.(type) {
	case *types.Pointer:
		inner, ok := ty.Elem().(*types.Named)
		if !ok {
			return false
		}
		return inner.Obj() == relayNamed.Obj()
	case *types.Named:
		return ty.Obj() == relayNamed.Obj()
	}
	return false
}

// structHoldsRelay reports whether the struct underlying named declares any
// field (named, embedded, or value) whose type is *Relay or Relay.
func structHoldsRelay(named *types.Named, relayNamed *types.Named) bool {
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		if fieldHoldsRelay(st.Field(i).Type(), relayNamed) {
			return true
		}
	}
	return false
}

// TestRELAY_SOLE_HOLDER_01 fails if any production named struct type
// satisfies kernel/lifecycle.ManagedResource AND holds a *Relay or Relay
// field, unless that type is runtime/bootstrap.relayAdapter. The positive
// control (relayAdapter must appear in the satisfying-AND-holding set)
// guards against vacuous passes after a typed-walk regression.
func TestRELAY_SOLE_HOLDER_01(t *testing.T) {
	t.Parallel()

	var relayNamed *types.Named
	var managedResourceIface *types.Interface
	var candidates []*types.Named

	_ = RunTypedProduction(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
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
			}
			// Collect every named struct in production so post-pass
			// analysis can intersect against the MR satisfying set + the
			// *Relay holder predicate. Generic origin types are skipped
			// (see §"Blind-spot inventory").
			for _, name := range p.Pkg.Scope().Names() {
				obj := p.Pkg.Scope().Lookup(name)
				if obj == nil {
					continue
				}
				named, ok := obj.Type().(*types.Named)
				if !ok {
					continue
				}
				if named.TypeParams() != nil && named.TypeArgs() == nil {
					continue // skip generic origin types
				}
				if _, isStruct := named.Underlying().(*types.Struct); !isStruct {
					continue
				}
				candidates = append(candidates, named)
			}
			return nil
		})

	require.NotNil(t, relayNamed,
		"RELAY-SOLE-HOLDER-01: failed to resolve %s.%s — production type-universe regression",
		relayIsoOutboxPkgPath, relayIsoRelayTypeName)
	require.NotNil(t, managedResourceIface,
		"RELAY-SOLE-HOLDER-01: failed to resolve %s.%s — production type-universe regression",
		relayIsoLifecyclePkgPath, relayIsoMRTypeName)
	require.NotEmpty(t, candidates,
		"RELAY-SOLE-HOLDER-01: collected zero candidate named structs — production type walk is empty, the filter is vacuous")

	sanctionedPkg := relayIsoBootstrapPkgPath
	sanctionedName := relayIsoAdapterTypeName
	adapterFound := false
	var violations []string

	for _, named := range candidates {
		if !typesutil.ImplementsInterface(types.NewPointer(named), managedResourceIface) {
			continue
		}
		if !structHoldsRelay(named, relayNamed) {
			continue
		}
		obj := named.Obj()
		if obj == nil || obj.Pkg() == nil {
			continue
		}
		if obj.Pkg().Path() == sanctionedPkg && obj.Name() == sanctionedName {
			adapterFound = true
			continue
		}
		violations = append(violations, obj.Pkg().Path()+"."+obj.Name())
	}

	require.True(t, adapterFound,
		"RELAY-SOLE-HOLDER-01 self-check: %s.%s no longer satisfies %s.%s "+
			"AND holds *%s.%s — the bootstrap relay funnel is broken; review "+
			"newRelayAdapter and its Checkers/Worker/Close methods.",
		sanctionedPkg, sanctionedName,
		relayIsoLifecyclePkgPath, relayIsoMRTypeName,
		relayIsoOutboxPkgPath, relayIsoRelayTypeName)

	for _, v := range violations {
		t.Errorf(
			"RELAY-SOLE-HOLDER-01: %s satisfies %s.%s AND holds %s.%s — only "+
				"%s.%s may be the sanctioned holder. Either remove the relay "+
				"field, route through %s.%s, or update the §\"single sanctioned "+
				"holder\" Hard 范本 in .claude/rules/gocell/ai-collab.md to admit "+
				"an additional holder (requires ADR amendment to "+
				"docs/architecture/202605201400-adr-relay-managedresource-isolation.md).",
			v, relayIsoLifecyclePkgPath, relayIsoMRTypeName,
			relayIsoOutboxPkgPath, relayIsoRelayTypeName,
			sanctionedPkg, sanctionedName,
			sanctionedPkg, sanctionedName,
		)
	}
}
