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

// WithFrameworkHTTPServing wires framework-owned HTTP serving (ownerCell:
// _framework). expected is the codegen-derived set of active framework-owned
// http contract ids this assembly MUST serve (generatedFrameworkServedContracts(),
// single-sourced from assembly.yaml frameworkContracts); routes are the actual
// RouteGroups the composition root constructs (e.g. cellmodules/deviceserving).
//
// phase0 fail-fasts (validateFrameworkServing, Init-independent, before serve)
// when the two sets disagree: an expected id with no wired RouteGroup is the
// DEAD-CONTRACT analog for framework serving (the contract is declared active +
// served but nothing mounts it — a silent dead route), and a wired route for a
// contract the assembly did not declare is stale wiring. This mirrors the gRPC
// permission-gate wiring fail-fast (#2204) and SPIRE catalog.Load /
// controller-runtime Builder.Build construction-time wiring checks: the static
// assembly makes the served set fully known before serve, so the absence of a
// backend is a startup error, not a request-time 404.
//
// Not calling this option leaves no framework HTTP routes mounted with NO error
// (an assembly with an empty frameworkContracts serves none). This is a wiring
// option (idempotent opt-in).
func WithFrameworkHTTPServing(expected []string, routes []FrameworkServedRoute) Option {
	return func(b *Bootstrap) {
		b.frameworkServedContractIDs = append([]string(nil), expected...)
		b.frameworkServingRoutes = append([]FrameworkServedRoute(nil), routes...)
	}
}

// validateFrameworkServing reconciles the codegen-derived must-serve expectation
// set against the wired framework RouteGroups. Called from phase0ValidateOptions
// (before any component starts), so an unwired-but-declared framework contract
// fails the process at startup rather than serving dead 404s.
func (b *Bootstrap) validateFrameworkServing() error {
	if len(b.frameworkServedContractIDs) == 0 && len(b.frameworkServingRoutes) == 0 {
		return nil
	}
	provided, err := b.frameworkServingProvidedSet()
	if err != nil {
		return err
	}
	expected := make(map[string]bool, len(b.frameworkServedContractIDs))
	for _, id := range b.frameworkServedContractIDs {
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
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"bootstrap: WithFrameworkHTTPServing: the codegen-derived framework served-contract "+
			"set (generatedFrameworkServedContracts) does not match the wired framework RouteGroups; "+
			"every active framework contract declared in assembly.frameworkContracts must have a "+
			"mounted RouteGroup, and every wired route must be declared",
		errcode.WithInternal(errcode.InternalAttr("_",
			fmt.Sprintf("missing_routes=%v extra_routes=%v", missing, extra))))
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
