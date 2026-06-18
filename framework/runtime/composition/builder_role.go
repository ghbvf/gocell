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
//   - explicit spec (split role): the closed set is spec.Colocated and only
//     modules whose ID is in it are mounted; other groups' cells are remote and
//     their modules are dropped (a nil module is skipped — Build then reports the
//     resulting missing colocated cell).
//
// assemblyCellIDs is the monolith branch's independent M12a closed-set source;
// the split branch uses spec.Colocated. Selecting the closed set per mode is the
// design, not a dead parameter.
//
// NewForRole is a convenience funnel, not the enforcement boundary: bijection /
// missing-module errors surface from Build.validateClosedSet, and the true
// fail-closed check is the bootstrap MOUNTED-EQUALS-COLOCATED phase guard, which
// catches any process that mounts a remote cell regardless of how it was built.
//
// ref: akka/akka cluster-sharding withRole — one artifact, a static role selects
// which cells this node hosts; non-hosted cells are reached as remote.
func NewForRole(assemblyCellIDs []string, spec bootstrap.DeploymentTopologySpec, allModules ...CellModule) *Builder {
	// Zero spec → monolith: full assembly closed set, all modules unfiltered.
	if len(spec.Colocated) == 0 && len(spec.Remote) == 0 {
		return New(assemblyCellIDs...).With(allModules...)
	}
	// Explicit spec → split: closed set = colocated subset; filter modules to it.
	colocated := make(map[string]struct{}, len(spec.Colocated))
	for _, id := range spec.Colocated {
		colocated[id] = struct{}{}
	}
	mounted := make([]CellModule, 0, len(spec.Colocated))
	for _, m := range allModules {
		if m == nil {
			continue // nil module → its colocated cell surfaces as missing in Build
		}
		if _, ok := colocated[m.ID()]; ok {
			mounted = append(mounted, m)
		}
	}
	return New(spec.Colocated...).With(mounted...)
}
