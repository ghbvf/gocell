package bootstrap

import (
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// FrameworkServedRoute pairs a framework-owned contract id with the RouteGroup
// that serves it. The explicit ContractID lets bootstrap reconcile the wired
// framework RouteGroups against the codegen-derived must-serve expectation set
// (generatedFrameworkServedContracts) — cell.RouteGroup carries the contract id
// only inside its Register closure (via auth.Mount), so it must be surfaced here
// for the startup reconcile.
//
// Group.CellID MUST be empty: a framework-owned contract has no cell (ADR
// 202606130635-1939 D4), and HTTP metrics attribute a CellID=="" RouteGroup to
// cell="_runtime" — correct for a framework-served endpoint not owned by a
// business cell.
type FrameworkServedRoute struct {
	ContractID string
	Group      cell.RouteGroup
}

// WithFrameworkHTTPServing wires the framework-owned HTTP RouteGroups (ownerCell:
// _framework) the composition root constructs (e.g. cellmodules/deviceserving).
//
// The must-serve EXPECTATION is NOT passed here — it rides on the assembly
// (assembly.Config.FrameworkContracts, codegen-derived from
// generatedFrameworkServedContracts() and threaded through buildAssembly), so it
// cannot be omitted: the assembly is a mandatory bootstrap input, whereas this
// option is not. bootstrap (validateFrameworkServing, phase0) reconciles the
// assembly's declared framework contracts against these routes. Omitting this
// option entirely while the assembly declares framework contracts is therefore a
// startup error (expected non-empty, provided empty), not a silent dead 404 —
// this closes the omit-option hole (#2348 review F1).
//
// phase0 fail-fast (Init-independent, before serve): an expected id (assembly
// frameworkContracts) with no wired RouteGroup is the DEAD-CONTRACT analog for
// framework serving; a wired route for a contract the assembly did not declare
// is stale wiring. Mirrors gRPC #2204 / SPIRE catalog.Load / controller-runtime
// Builder.Build construction-time wiring checks.
func WithFrameworkHTTPServing(routes []FrameworkServedRoute) Option {
	return func(b *Bootstrap) {
		b.frameworkServingRoutes = append([]FrameworkServedRoute(nil), routes...)
	}
}

// frameworkServedExpected is the must-serve expectation set, derived from the
// assembly (assembly.Config.FrameworkContracts via WithAssembly). It rides on the
// mandatory assembly so it cannot be omitted — the basis for the omit-option
// fail-fast. nil when no assembly is wired (auto-built path) or the assembly
// declares no framework contracts.
func (b *Bootstrap) frameworkServedExpected() []string {
	if b.assemblyCore == nil {
		return nil
	}
	return b.assemblyCore.FrameworkContracts()
}

// validateFrameworkServing reconciles the assembly's codegen-derived must-serve
// expectation set (frameworkServedExpected) against the wired framework
// RouteGroups. Called from phase0ValidateOptions (before any component starts),
// so an unwired-but-declared framework contract fails the process at startup
// rather than serving dead 404s.
func (b *Bootstrap) validateFrameworkServing() error {
	expectedIDs := b.frameworkServedExpected()
	if len(expectedIDs) == 0 && len(b.frameworkServingRoutes) == 0 {
		return nil
	}
	provided, err := b.frameworkServingProvidedSet()
	if err != nil {
		return err
	}
	expected := make(map[string]bool, len(expectedIDs))
	for _, id := range expectedIDs {
		expected[id] = true
	}
	var missing, extra []string
	for id := range expected {
		if !provided[id] {
			missing = append(missing, id)
		}
	}
	for id := range provided {
		if !expected[id] {
			extra = append(extra, id)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var opts []errcode.Option
	if len(missing) > 0 {
		opts = append(opts, errcode.WithInternal(errcode.InternalAttr("missing_contracts", fmt.Sprintf("%v", missing))))
	}
	if len(extra) > 0 {
		opts = append(opts, errcode.WithInternal(errcode.InternalAttr("extra_routes", fmt.Sprintf("%v", extra))))
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"bootstrap: WithFrameworkHTTPServing: the assembly.frameworkContracts (codegen-derived must-serve set) "+
			"does not match the wired framework RouteGroups; "+
			"every active framework contract declared in assembly.frameworkContracts must have a "+
			"mounted RouteGroup, and every wired route must be declared",
		opts...)
}

// frameworkServingProvidedSet validates each wired route's shape (non-empty
// ContractID, framework-owned empty CellID, non-nil Register, no duplicates) and
// returns the set of provided contract ids.
func (b *Bootstrap) frameworkServingProvidedSet() (map[string]bool, error) {
	provided := make(map[string]bool, len(b.frameworkServingRoutes))
	for i := range b.frameworkServingRoutes {
		r := b.frameworkServingRoutes[i]
		switch {
		case r.ContractID == "":
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"bootstrap: WithFrameworkHTTPServing: framework-served route has an empty ContractID")
		case r.Group.CellID != "":
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"bootstrap: WithFrameworkHTTPServing: framework-served route must have an empty CellID "+
					"(framework-owned contracts have no cell)",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q cellID=%q", r.ContractID, r.Group.CellID))))
		case r.Group.Register == nil:
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"bootstrap: WithFrameworkHTTPServing: framework-served route has a nil Register function",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q", r.ContractID))))
		case provided[r.ContractID]:
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"bootstrap: WithFrameworkHTTPServing: duplicate framework-served route for one contract",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q", r.ContractID))))
		}
		provided[r.ContractID] = true
	}
	return provided, nil
}

// frameworkServingRouteGroups returns the wired framework RouteGroups for phase5
// collection (CellID stays empty — framework-owned). Returns nil when none.
func (b *Bootstrap) frameworkServingRouteGroups() []cell.RouteGroup {
	if len(b.frameworkServingRoutes) == 0 {
		return nil
	}
	groups := make([]cell.RouteGroup, 0, len(b.frameworkServingRoutes))
	for i := range b.frameworkServingRoutes {
		groups = append(groups, b.frameworkServingRoutes[i].Group)
	}
	return groups
}
