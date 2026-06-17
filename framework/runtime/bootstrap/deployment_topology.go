package bootstrap

// deployment_topology.go — WriteOnce deployment-topology decision API.
//
// DISTINCT from topology.go (adapter topology: adapterMode/storageBackend).
// This file manages DEPLOYMENT topology: which cells are co-located in the
// same process versus hosted remotely.
//
// ref: kubernetes-sigs/controller-runtime — single-constructor validated type;
// zero value is safe (all colocated = single-process default).
// ref: uber-go/fx app.go — sealed singleton constructed once at startup,
// read-only during serving.

import (
	"strings"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/netutil"
)

// RemoteCellEndpoint names a cell hosted in another process and its endpoint.
type RemoteCellEndpoint struct {
	CellID   string
	Endpoint string
}

// DeploymentTopologySpec is the PLAIN, codegen-produced input describing the
// assembly's deployment placement (single-sourced from assembly.yaml topology,
// emitted by `gocell generate` as generatedDeploymentTopology()). It carries no
// validation — phase0 seals+validates it into a DeploymentTopology. Empty spec
// => all cells co-located (zero-migration default).
type DeploymentTopologySpec struct {
	Colocated []string
	Remote    []RemoteCellEndpoint
}

// DeploymentTopology is the SEALED, validated, runtime-read-only view of the
// deployment placement. Constructed once at phase0 (WriteOnce) from a Spec;
// queried during request/dispatch routing. Distinct concern from the adapter
// Topology (this file vs topology.go). Sealed: all fields unexported, sole
// constructor newDeploymentTopology. Zero value = all-colocated (no explicit
// topology) — IsColocated true for any cellID, no remotes.
//
// Field set frozen by TestDeploymentTopologyZeroExportedFields
// (DEPLOYMENT-TOPOLOGY-SEALED-FIELD-FROZEN-01).
//
// ref: uber-go/fx app.go — sealed singleton constructed once, safe zero value.
type DeploymentTopology struct {
	explicit  bool                // false = no topology declared => all colocated
	colocated map[string]struct{} // populated iff explicit
	remote    map[string]string   // cellID -> endpoint, iff explicit
}

// Deployment topology error message constants — MESSAGE-CONST-LITERAL-01.
const (
	errMsgDeployTopoMutualExclusion = "deployment topology: cell appears in both colocated and remote"
	errMsgDeployTopoDuplicateCellID = "deployment topology: duplicate cellID"
	errMsgDeployTopoEmptyCellID     = "deployment topology: cellID must not be empty or whitespace"
	errMsgDeployTopoEmptyEndpoint   = "deployment topology: remote endpoint must not be empty"
	errMsgDeployTopoInvalidEndpoint = "deployment topology: remote endpoint must be host:port or an http/https URL"
)

// newDeploymentTopology validates spec-internal consistency and seals it.
//
// Errors (errcode, const-literal msg) on:
//   - cell in both colocated and remote (mutual exclusion)
//   - duplicate cellID in colocated or remote
//   - empty/whitespace cellID
//   - empty endpoint
//   - malformed endpoint (not a bare host:port or http/https URL with non-empty host)
//
// It does NOT check membership in an assembly cell set (that is the static
// gocell-validate gate TOPO-10; bootstrap has no cell set).
// explicit = len(Colocated)+len(Remote) > 0.
func newDeploymentTopology(spec DeploymentTopologySpec) (DeploymentTopology, error) {
	if len(spec.Colocated) == 0 && len(spec.Remote) == 0 {
		return DeploymentTopology{}, nil // zero value: all-colocated
	}

	colocated, err := buildDeployColocatedSet(spec.Colocated)
	if err != nil {
		return DeploymentTopology{}, err
	}

	remote, err := buildDeployRemoteMap(spec.Remote, colocated)
	if err != nil {
		return DeploymentTopology{}, err
	}

	return DeploymentTopology{
		explicit:  true,
		colocated: colocated,
		remote:    remote,
	}, nil
}

// NewDeploymentTopology seals + validates a DeploymentTopologySpec into the
// runtime-read-only DeploymentTopology. It is the SINGLE sealed-semantics entry
// for composition-root consumers that must classify a cell's placement BEFORE
// phase0 — e.g. accesscore selecting an in-process vs remote transport at module
// wiring. phase0 seals the same spec via the same internal constructor, so both
// see identical IsColocated / RemoteEndpoint semantics (no parallel topology
// truth re-derived from the raw spec).
func NewDeploymentTopology(spec DeploymentTopologySpec) (DeploymentTopology, error) {
	return newDeploymentTopology(spec)
}

// buildDeployColocatedSet validates colocated entries and builds the set.
// Extracted to keep newDeploymentTopology cognitive complexity ≤ 15.
func buildDeployColocatedSet(colocated []string) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(colocated))
	for _, id := range colocated {
		if strings.TrimSpace(id) == "" {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				errMsgDeployTopoEmptyCellID,
				errcode.WithInternal(errcode.InternalAttr("cellID", id)))
		}
		if _, dup := seen[id]; dup {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				errMsgDeployTopoDuplicateCellID,
				errcode.WithInternal(errcode.InternalAttr("cellID", id)),
				errcode.WithDetails(errcode.PublicString("cellID", id)))
		}
		seen[id] = struct{}{}
	}
	return seen, nil
}

// buildDeployRemoteMap validates remote entries and builds the map.
// Extracted to keep newDeploymentTopology cognitive complexity ≤ 15.
func buildDeployRemoteMap(remote []RemoteCellEndpoint, colocated map[string]struct{}) (map[string]string, error) {
	seen := make(map[string]struct{}, len(remote))
	result := make(map[string]string, len(remote))
	for _, entry := range remote {
		id := entry.CellID
		if strings.TrimSpace(id) == "" {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				errMsgDeployTopoEmptyCellID,
				errcode.WithInternal(errcode.InternalAttr("cellID", id)))
		}
		if _, dup := seen[id]; dup {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				errMsgDeployTopoDuplicateCellID,
				errcode.WithInternal(errcode.InternalAttr("cellID", id)),
				errcode.WithDetails(errcode.PublicString("cellID", id)))
		}
		if _, inColoc := colocated[id]; inColoc {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				errMsgDeployTopoMutualExclusion,
				errcode.WithInternal(errcode.InternalAttr("cellID", id)),
				errcode.WithDetails(errcode.PublicString("cellID", id)))
		}
		if err := validateDeployEndpoint(entry.Endpoint, id); err != nil {
			return nil, err
		}
		seen[id] = struct{}{}
		result[id] = entry.Endpoint
	}
	return result, nil
}

// validateDeployEndpoint checks that ep is non-empty and a valid network
// address (netutil.IsValidNetworkAddress: bare host:port OR http/https URL with host).
func validateDeployEndpoint(ep, cellID string) error {
	if strings.TrimSpace(ep) == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			errMsgDeployTopoEmptyEndpoint,
			errcode.WithInternal(errcode.InternalAttr("cellID", cellID)),
			errcode.WithDetails(errcode.PublicString("cellID", cellID)))
	}
	if netutil.IsValidNetworkAddress(ep) {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		errMsgDeployTopoInvalidEndpoint,
		errcode.WithInternal(errcode.InternalAttr("cellID", cellID), errcode.InternalAttr("endpoint", ep)),
		errcode.WithDetails(errcode.PublicString("cellID", cellID)))
}

// HasRemoteCells reports whether the deployment topology declares at least one
// remote cell (i.e. a split topology with a process boundary). Zero value
// (no explicit topology, all-colocated) returns false. Used by the bootstrap
// phase0 broker-mandatory gate: a split topology + in-memory EventBus is
// rejected because the in-memory bus cannot deliver events across processes.
//
// Coarse proxy (Medium, blind-spot): true for ANY remote cell, even one with
// only sync (CellTransport) contracts and no cross-process events; the precise
// "has cross-process event pub/sub" signal would require codegen derivation
// (tracked as #1967).
func (t DeploymentTopology) HasRemoteCells() bool {
	return len(t.remote) > 0
}

// HasNonLoopbackRemoteCells reports whether the topology declares at least one
// remote cell whose endpoint is NOT a loopback address (per
// netutil.IsLoopbackEndpoint). This is the split-topology mTLS trigger (#2263):
// a non-loopback remote peer crosses a real network boundary, so the cross-cell
// transport MUST use mTLS — cellmodules/celltls.Resolve fails closed when this is
// true but no TLS material is configured. A loopback-only split (local
// multi-process dev) returns false and stays plaintext-eligible.
//
// Zero value (all-colocated) returns false.
func (t DeploymentTopology) HasNonLoopbackRemoteCells() bool {
	for _, ep := range t.remote {
		if !netutil.IsLoopbackEndpoint(ep) {
			return true
		}
	}
	return false
}

// IsColocated reports whether cellID is co-located in the same process.
// For a zero-value (no explicit topology), always returns true — all cells
// are treated as colocated (single-process default).
//
// In explicit mode, a cellID that is neither colocated nor a remote entry
// yields IsColocated()==false AND RemoteEndpoint()==("",false) — i.e. "not in
// this topology". For CONSUMED contracts this cannot occur at runtime because
// `gocell validate` TOPO-11 statically rejects unreachable providers; callers
// treating both-false as a misconfiguration is correct.
func (t DeploymentTopology) IsColocated(cellID string) bool {
	if !t.explicit {
		return true
	}
	_, ok := t.colocated[cellID]
	return ok
}

// RemoteEndpoint returns the remote network endpoint for cellID and true iff
// cellID is declared as a remote cell. For a zero-value (no explicit topology),
// always returns ("", false).
//
// In explicit mode, a cellID that is neither colocated nor a remote entry
// yields IsColocated()==false AND RemoteEndpoint()==("",false) — i.e. "not in
// this topology". For CONSUMED contracts this cannot occur at runtime because
// `gocell validate` TOPO-11 statically rejects unreachable providers; callers
// treating both-false as a misconfiguration is correct.
func (t DeploymentTopology) RemoteEndpoint(cellID string) (string, bool) {
	if !t.explicit {
		return "", false
	}
	ep, ok := t.remote[cellID]
	return ep, ok
}
