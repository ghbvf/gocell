package metadata

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/netutil"
)

// TopologyMeta is the OPTIONAL assembly.yaml `topology` section: the COMPLETE
// deployment-partition graph of the assembly's cells. Empty/absent => all cells
// co-located (zero-migration default, single-process monolith). When non-empty
// it declares one group per deployment role; the groups MUST exhaustively and
// mutually-exclusively partition the assembly's cells (every cell in exactly one
// group). A process selects ONE role at startup (GOCELL_CELL_ROLE, #1423 PR-2):
// that group's cells run in-process, every other group's cells are remote peers
// reachable at their declared endpoint.
//
// This is DEPLOYMENT topology — distinct from the runtime adapter Topology
// (adapterMode/storageBackend) in runtime/bootstrap.
type TopologyMeta struct {
	Groups []TopologyGroup `yaml:"groups,omitempty"`
}

// TopologyGroup is one deployment role: a named set of cells deployed together
// in one process, reachable by peers at Endpoint. Roles are unique within an
// assembly; the cell sets partition the assembly's cells.
//
// ref: akka cluster — node roles select which actors a single artifact hosts.
type TopologyGroup struct {
	Role     string   `yaml:"role"`
	Cells    []string `yaml:"cells"`
	Endpoint string   `yaml:"endpoint"`
}

// CellGroup returns the group that hosts cellID and true, or the zero group and
// false when no declared group contains it (including the empty-topology case:
// no groups => all cells co-located, not assigned to any explicit group).
func CellGroup(asm *AssemblyMeta, cellID string) (TopologyGroup, bool) {
	for _, g := range asm.Topology.Groups {
		for _, c := range g.Cells {
			if c == cellID {
				return g, true
			}
		}
	}
	return TopologyGroup{}, false
}

// Error message constants — MESSAGE-CONST-LITERAL-01 compliance.
const (
	errMsgTopoEmptyRole       = "topology: group role must be non-empty"
	errMsgTopoDuplicateRole   = "topology: duplicate group role"
	errMsgTopoEmptyCells      = "topology: group must declare at least one cell"
	errMsgTopoDuplicateCell   = "topology: cell appears in more than one group"
	errMsgTopoUnknownCell     = "topology: group cell not declared in assembly cells"
	errMsgTopoNonExhaustive   = "topology: assembly cell is not assigned to any group (non-exhaustive partition)"
	errMsgTopoEmptyEndpoint   = "topology: empty endpoint — group endpoint must be non-blank"
	errMsgTopoInvalidEndpoint = "topology: invalid endpoint — must be host:port or http/https URL with non-empty host"
)

// ValidateTopologyStructure validates asm.Topology's internal consistency,
// returning an errcode error on the first violation (nil if valid / empty):
//   - every group has a non-empty, assembly-unique role
//   - every group declares at least one cell
//   - every referenced cellID ∈ asm.Cells (metadata.CellIDs(asm.Cells))
//   - no cellID appears in more than one group (and no repeats within a group)
//   - the groups exhaustively partition asm.Cells (every cell assigned)
//   - every group endpoint is a syntactically valid network address (bare
//     host:port OR http/https URL with non-empty host, per
//     netutil.IsValidNetworkAddress); empty/whitespace/unparseable => error.
//     Every group is addressable: from any other role's perspective it is a
//     remote peer, so an endpoint is always required.
//
// Empty topology (no groups) => nil (all-colocated default).
// The error carries WithDetails("field", ...) plus "cellID" or "role" for
// structured TOPO-10 extraction.
func ValidateTopologyStructure(asm *AssemblyMeta) error {
	groups := asm.Topology.Groups
	if len(groups) == 0 {
		return nil
	}

	known := buildCellSet(asm)
	seenRoles := make(map[string]struct{}, len(groups))
	seenCells := make(map[string]struct{}, len(known))

	for i, g := range groups {
		if err := validateGroup(i, g, known, seenRoles, seenCells); err != nil {
			return err
		}
	}

	return checkExhaustive(asm, seenCells)
}

// buildCellSet builds a set from asm.Cells IDs.
func buildCellSet(asm *AssemblyMeta) map[string]struct{} {
	ids := CellIDs(asm.Cells)
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// validateGroup checks one group's role, cells, and endpoint, recording its role
// and cells into the shared seen-sets so later groups detect duplicates.
// Extracted to keep ValidateTopologyStructure cognitive complexity ≤ 15.
func validateGroup(i int, g TopologyGroup, known, seenRoles, seenCells map[string]struct{}) error {
	rolePath := fmt.Sprintf("topology.groups[%d].role", i)
	if strings.TrimSpace(g.Role) == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			errMsgTopoEmptyRole, topoRoleDetail(g.Role, rolePath))
	}
	if _, dup := seenRoles[g.Role]; dup {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			errMsgTopoDuplicateRole, topoRoleDetail(g.Role, rolePath))
	}
	seenRoles[g.Role] = struct{}{}

	if len(g.Cells) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			errMsgTopoEmptyCells, topoRoleDetail(g.Role, fmt.Sprintf("topology.groups[%d].cells", i)))
	}
	if err := validateEndpoint(g.Endpoint, g.Role, fmt.Sprintf("topology.groups[%d].endpoint", i)); err != nil {
		return err
	}

	for j, c := range g.Cells {
		cellPath := fmt.Sprintf("topology.groups[%d].cells[%d]", i, j)
		if _, ok := known[c]; !ok {
			return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoUnknownCell, topoCellDetail(c, cellPath))
		}
		if _, dup := seenCells[c]; dup {
			return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoDuplicateCell, topoCellDetail(c, cellPath))
		}
		seenCells[c] = struct{}{}
	}
	return nil
}

// topoRoleDetail builds the WithDetails option anchoring a topology error at a
// role/group field. Returning the option (not the whole error) keeps the
// const-literal message at the errcode.New call site — MESSAGE-CONST-LITERAL-01
// rejects a message funneled through a function parameter.
func topoRoleDetail(role, fieldPath string) errcode.Option {
	return errcode.WithDetails(
		errcode.PublicString("role", role),
		errcode.PublicString("field", fieldPath),
	)
}

// topoCellDetail builds the WithDetails option anchoring a topology error at a
// cell field (see topoRoleDetail on why this returns the option, not the error).
func topoCellDetail(cellID, fieldPath string) errcode.Option {
	return errcode.WithDetails(
		errcode.PublicString("cellID", cellID),
		errcode.PublicString("field", fieldPath),
	)
}

// validateEndpoint checks that ep is non-empty, non-whitespace, and either a
// valid http/https URL or a bare host:port (netutil.IsValidNetworkAddress).
// role is the owning group's role; fieldPath is the precise topology field path
// (e.g. "topology.groups[0].endpoint") for structured diagnostic output.
func validateEndpoint(ep, role, fieldPath string) error {
	if strings.TrimSpace(ep) == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			errMsgTopoEmptyEndpoint,
			errcode.WithDetails(
				errcode.PublicString("role", role),
				errcode.PublicString("field", fieldPath),
			))
	}
	if netutil.IsValidNetworkAddress(ep) {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
		errMsgTopoInvalidEndpoint,
		errcode.WithDetails(
			errcode.PublicString("role", role),
			errcode.PublicString("endpoint", ep),
			errcode.PublicString("field", fieldPath),
		))
}

// checkExhaustive verifies every cell in asm.Cells is assigned to some group.
// Iterates the declared cell order (not the set) for deterministic diagnostics.
func checkExhaustive(asm *AssemblyMeta, seenCells map[string]struct{}) error {
	for _, id := range CellIDs(asm.Cells) {
		if _, ok := seenCells[id]; !ok {
			return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoNonExhaustive, topoCellDetail(id, "topology"))
		}
	}
	return nil
}
