package composition

import (
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// NewForRole returns a Builder that mounts only the cells THIS process hosts for
// its deployment role — the colocated subset of the assembly. It is the
// sanctioned subset-mount entry for production composition roots (cmd/*,
// examples/*): COMPOSITION-NEWFORROLE-FUNNEL-01 bans bare composition.New there
// so a root cannot mount the full cell set in a split topology.
//
// spec is the per-process placement derived once at the composition root via
// bootstrap.SpecForRole(generatedTopologyGroups(), GOCELL_CELL_ROLE) and also
// carried on SharedDeps.DeploymentTopology (single source — NewForRole consumes
// it, it does not re-derive). Two branches:
//
//   - zero spec (all-colocated monolith): mount EVERY module unfiltered, with the
//     full assemblyCellIDs as the closed set. Filtering here would silently drop
//     an out-of-assembly module instead of letting Build's M12a closed-set guard
//     (validateClosedSet) reject it; the monolith path must stay unfiltered so
//     drift between generatedCellModules and the assembly cell list still fails.
//   - explicit spec (split role): the closed set is spec.Colocated. Modules whose
//     cell is declared remote for this role are dropped (they are reached via
//     transport); everything else — colocated modules PLUS any stranger whose cell
//     is in neither colocated nor remote — is passed to Build, whose closed-set
//     guard (closed set = spec.Colocated) mounts the colocated bijection and
//     rejects a stranger ("not in the assembly closed set"). A nil module is
//     passed through unchanged so Build's nil-guard fail-fasts on it (never
//     silently dropped). Dropping only the known-remote set (rather than keeping
//     only the colocated set) is deliberate: it routes stranger drift —
//     generatedCellModules out of sync with the topology groups — into Build's
//     fail-fast instead of a silent drop (#2278 review).
//
// The spec is recorded on the Builder (roleSpec); Build asserts it equals
// SharedDeps.DeploymentTopology so the subset-mount source and the sealed runtime
// topology cannot diverge (single deployment-topology source).
//
// A dropped (remote) module's Provide is never called, so a module MUST open its
// infrastructure in Provide, not in its constructor — a constructor that opened a
// connection would leak it for a remote cell this process never builds.
//
// assemblyCellIDs is the monolith branch's independent M12a closed-set source;
// the split branch uses spec.Colocated. Selecting the closed set per mode is the
// design, not a dead parameter.
//
// NewForRole is a convenience funnel, not the enforcement boundary. The two-sided
// grading: the caller-funnel COMPOSITION-NEWFORROLE-FUNNEL-01 (Medium, AST scan)
// bans bare composition.New in wiring roots, and the bootstrap
// MOUNTED-EQUALS-COLOCATED-01 phase guard (Medium, runtime bijection) is the true
// fail-closed backstop — it catches any process that mounts a remote cell
// regardless of how it was built. Bijection / missing-module / stranger errors
// surface from Build.validateClosedSet. Both ceilings are Medium (exported
// cross-package constructor + runtime cellID strings), per ADR §#1967 Amendment.
//
// ref: akka/akka cluster-sharding withRole — one artifact, a static role selects
// which cells this node hosts; non-hosted cells are reached as remote.
func NewForRole(assemblyCellIDs []string, spec bootstrap.DeploymentTopologySpec, allModules ...CellModule) *Builder {
	// Zero spec → monolith: full assembly closed set, all modules unfiltered.
	if len(spec.Colocated) == 0 && len(spec.Remote) == 0 {
		b := New(assemblyCellIDs...).With(allModules...)
		b.roleSpec = &spec
		return b
	}
	// Explicit spec → split: closed set = colocated subset. Drop ONLY the
	// known-remote modules; pass colocated + any stranger to Build so its
	// closed-set guard rejects a stranger (drift) instead of silently dropping it.
	remote := make(map[string]struct{}, len(spec.Remote))
	for _, r := range spec.Remote {
		remote[r.CellID] = struct{}{}
	}
	mounted := make([]CellModule, 0, len(allModules))
	for _, m := range allModules {
		if m == nil {
			// Pass nil through to Build, whose resolveModuleResult fail-fasts on a
			// nil module ("module list contains nil"). NewForRole must not silently
			// drop a wiring bug — keep the same non-nil contract the monolith branch
			// (and plain New) rely on (#2278 review F1).
			mounted = append(mounted, m)
			continue
		}
		if _, isRemote := remote[m.ID()]; isRemote {
			continue // remote cell: hosted by another role, reached via transport
		}
		mounted = append(mounted, m)
	}
	b := New(spec.Colocated...).With(mounted...)
	b.roleSpec = &spec
	return b
}
