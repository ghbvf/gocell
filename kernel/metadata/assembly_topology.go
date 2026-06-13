package metadata

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TopologyMeta is the OPTIONAL assembly.yaml `topology` section: the deployment
// placement of the assembly's cells. Empty/absent => all cells co-located
// (zero-migration default). When non-empty it MUST exhaustively partition the
// assembly's cells: every cell in exactly one of colocated/remote (mutual
// exclusion + full coverage). This is DEPLOYMENT topology — distinct from the
// runtime adapter Topology (adapterMode/storageBackend) in runtime/bootstrap.
type TopologyMeta struct {
	Colocated []string              `yaml:"colocated,omitempty"`
	Remote    []TopologyRemoteEntry `yaml:"remote,omitempty"`
}

// TopologyRemoteEntry describes a cell that is deployed as a separate remote
// service reachable at Endpoint.
type TopologyRemoteEntry struct {
	CellID   string `yaml:"cellID"`
	Endpoint string `yaml:"endpoint"`
}

// CellLocationKind is the placement classification of a cell relative to an
// assembly's topology.
type CellLocationKind int

const (
	// CellLocationMissing is the zero value — fail-closed default. Used when
	// the cellID is not found in the assembly cells or topology at all.
	CellLocationMissing CellLocationKind = iota
	// CellLocationLocal means the cell is co-located in the same process.
	CellLocationLocal
	// CellLocationRemote means the cell is a separate deployment reachable via
	// a network endpoint.
	CellLocationRemote
)

// CellLocation is the result of ClassifyCell: where a cell lives relative to
// the current assembly boundary.
type CellLocation struct {
	Kind     CellLocationKind
	Endpoint string // populated iff Kind == CellLocationRemote
}

// IsLocal reports whether the cell is co-located in the same process.
func (l CellLocation) IsLocal() bool { return l.Kind == CellLocationLocal }

// IsMissing reports whether the cell was not found in the assembly or topology.
func (l CellLocation) IsMissing() bool { return l.Kind == CellLocationMissing }

// RemoteEndpoint returns the remote endpoint and true iff Kind == CellLocationRemote.
func (l CellLocation) RemoteEndpoint() (string, bool) {
	if l.Kind != CellLocationRemote {
		return "", false
	}
	return l.Endpoint, true
}

// ClassifyCell classifies cellID's placement relative to asm's topology.
//
// Empty topology: Local if cellID ∈ asm.Cells, else Missing.
// Non-empty topology: Local if ∈ colocated; Remote(endpoint) if ∈ remote; else Missing.
func ClassifyCell(asm *AssemblyMeta, cellID string) CellLocation {
	topo := asm.Topology
	if len(topo.Colocated) == 0 && len(topo.Remote) == 0 {
		return classifyEmptyTopology(asm, cellID)
	}
	return classifyNonEmptyTopology(topo, cellID)
}

func classifyEmptyTopology(asm *AssemblyMeta, cellID string) CellLocation {
	for _, id := range CellIDs(asm.Cells) {
		if id == cellID {
			return CellLocation{Kind: CellLocationLocal}
		}
	}
	return CellLocation{Kind: CellLocationMissing}
}

func classifyNonEmptyTopology(topo TopologyMeta, cellID string) CellLocation {
	for _, id := range topo.Colocated {
		if id == cellID {
			return CellLocation{Kind: CellLocationLocal}
		}
	}
	for _, entry := range topo.Remote {
		if entry.CellID == cellID {
			return CellLocation{Kind: CellLocationRemote, Endpoint: entry.Endpoint}
		}
	}
	return CellLocation{Kind: CellLocationMissing}
}

// Error message constants — MESSAGE-CONST-LITERAL-01 compliance.
const (
	errMsgTopoMutualExclusion = "topology mutual exclusion violation: cell appears in both colocated and remote"
	errMsgTopoDuplicateColoc  = "topology duplicate cellID in colocated"
	errMsgTopoDuplicateRemote = "topology duplicate cellID in remote"
	errMsgTopoUnknownColoc    = "topology colocated references unknown cellID"
	errMsgTopoUnknownRemote   = "topology remote references unknown cellID"
	errMsgTopoNonExhaustive   = "topology is non-exhaustive: cell in assembly is not classified"
	errMsgTopoEmptyEndpoint   = "topology remote endpoint is empty or whitespace"
	errMsgTopoInvalidEndpoint = "topology remote endpoint is not a valid network address"
)

// ValidateTopologyStructure validates asm.Topology's internal consistency,
// returning an errcode error on the first violation (nil if valid / empty):
//   - mutual exclusion: no cell in both colocated and remote
//   - no duplicate cellID within colocated or within remote
//   - every referenced cellID ∈ asm.Cells (use metadata.CellIDs(asm.Cells))
//   - non-empty topology => exhaustive: colocated ∪ remote == set(asm.Cells)
//   - each remote.endpoint is a syntactically valid network address
//     (net.SplitHostPort succeeds, OR url.Parse succeeds with a non-empty Host);
//     empty/whitespace/unparseable => error
//
// Empty topology (no colocated, no remote) => nil (all-colocated default).
func ValidateTopologyStructure(asm *AssemblyMeta) error {
	topo := asm.Topology
	if len(topo.Colocated) == 0 && len(topo.Remote) == 0 {
		return nil
	}

	knownCells := buildCellSet(asm)

	// Build colocated set (check duplicates).
	colocSet, err := buildColocatedSet(topo.Colocated, knownCells)
	if err != nil {
		return err
	}

	// Validate remote entries (duplicates, unknown, endpoint).
	remoteSet, err := buildRemoteSet(topo.Remote, knownCells, colocSet)
	if err != nil {
		return err
	}

	// Exhaustiveness check: every cell in asm.Cells must appear in exactly one list.
	return checkExhaustive(knownCells, colocSet, remoteSet)
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

// buildColocatedSet checks for duplicates and unknown cells; returns the set.
func buildColocatedSet(colocated []string, known map[string]struct{}) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(colocated))
	for _, id := range colocated {
		if _, dup := seen[id]; dup {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoDuplicateColoc,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("duplicate colocated cellID=%q", id))))
		}
		seen[id] = struct{}{}
		if _, ok := known[id]; !ok {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoUnknownColoc,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("unknown colocated cellID=%q", id))))
		}
	}
	return seen, nil
}

// buildRemoteSet checks for duplicates, unknown cells, mutual exclusion and
// endpoint validity; returns the remote cellID set.
func buildRemoteSet(remote []TopologyRemoteEntry, known, colocSet map[string]struct{}) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(remote))
	for _, entry := range remote {
		id := entry.CellID
		if _, dup := seen[id]; dup {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoDuplicateRemote,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("duplicate remote cellID=%q", id))))
		}
		seen[id] = struct{}{}
		if _, ok := known[id]; !ok {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoUnknownRemote,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("unknown remote cellID=%q", id))))
		}
		if _, inColoc := colocSet[id]; inColoc {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoMutualExclusion,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("mutual exclusion cellID=%q appears in both colocated and remote", id))))
		}
		if err := validateEndpoint(entry.Endpoint, id); err != nil {
			return nil, err
		}
	}
	return seen, nil
}

// validateEndpoint checks that ep is non-empty, non-whitespace, and is either
// a valid host:port (net.SplitHostPort) or a URL with a non-empty Host.
func validateEndpoint(ep, cellID string) error {
	if strings.TrimSpace(ep) == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			errMsgTopoEmptyEndpoint,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("empty endpoint cellID=%q", cellID))))
	}
	if isValidNetworkAddress(ep) {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
		errMsgTopoInvalidEndpoint,
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("invalid endpoint cellID=%q endpoint=%q", cellID, ep))))
}

// isValidNetworkAddress returns true if ep is a valid host:port pair or a URL
// with a non-empty Host component.
func isValidNetworkAddress(ep string) bool {
	// Try host:port first (covers "host:8080", "[::1]:9000", etc.)
	if host, _, err := net.SplitHostPort(ep); err == nil && host != "" {
		return true
	}
	// Try as a URL (covers "https://remote.svc/", "grpc://host:9000", etc.)
	if u, err := url.Parse(ep); err == nil && u.Host != "" {
		return true
	}
	return false
}

// checkExhaustive verifies that every cell in known appears in either colocSet
// or remoteSet (but not both — mutual exclusion is already checked earlier).
func checkExhaustive(known, colocSet, remoteSet map[string]struct{}) error {
	for id := range known {
		_, inColoc := colocSet[id]
		_, inRemote := remoteSet[id]
		if !inColoc && !inRemote {
			return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				errMsgTopoNonExhaustive,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("non-exhaustive topology cellID=%q not classified", id))))
		}
	}
	return nil
}
