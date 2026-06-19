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
	"sort"
	"strings"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/netutil"
)

// RemoteCellEndpoint names a cell hosted in another process and its endpoint.
type RemoteCellEndpoint struct {
	CellID   string
	Endpoint string
}

// TopologyGroup is the codegen-produced runtime mirror of metadata.TopologyGroup:
// one deployment role (a named set of cells deployed together, reachable at
// Endpoint). The full group graph is single-sourced from assembly.yaml topology
// and emitted by `gocell generate` as generatedTopologyGroups(). A process picks
// one role at startup and SpecForRole derives its per-process
// DeploymentTopologySpec. Distinct (layer-mirrored) from metadata.TopologyGroup
// because the kernel cannot import runtime/bootstrap — the same layer-split
// rationale as the RemoteCellEndpoint mirror below.
type TopologyGroup struct {
	Role     string
	Cells    []string
	Endpoint string
	// RequiresBrokerForCrossProcessEvents is the codegen-derived per-role signal:
	// true iff this group is an endpoint (publisher OR subscriber side) of at
	// least one active, amqp-transported event contract whose publisher cell and
	// some subscriber cell fall in DIFFERENT groups (i.e. cross a process
	// boundary). Derived by `gocell generate assembly` from the assembly's
	// contractUsages + topology, NOT hand-authored — metadata.TopologyGroup
	// (authoring YAML) deliberately does not carry this field. It is the precise
	// input to the phase0 broker-mandatory gate, replacing the coarse
	// HasRemoteCells proxy: a sync-only split (remote cells but no cross-process
	// events) reports false here and is correctly allowed an in-memory bus (#2196).
	RequiresBrokerForCrossProcessEvents bool
}

// Deployment-role selection error message constants — MESSAGE-CONST-LITERAL-01.
const (
	errMsgDeployTopoRoleRequired = "deployment topology: GOCELL_CELL_ROLE must be set when the assembly declares " +
		"multiple deployment groups; a multi-group topology is a split deployment, so this process must select its role"
	errMsgDeployTopoUnknownRole = "deployment topology: GOCELL_CELL_ROLE names a role not declared in the assembly topology groups"
)

// SpecForRole derives the per-process DeploymentTopologySpec from the full
// topology group graph for the deployment role this process runs as (selected at
// startup by GOCELL_CELL_ROLE, WriteOnce).
//
//   - empty role + 0/1 group → the zero (all-colocated monolith) spec: every
//     cell mounted in one process, the single-binary zero-migration default #1423
//     preserves.
//   - empty role + ≥2 groups → fail-closed: a multi-group topology declares an
//     intended split, so running it with no role is a misconfiguration that a
//     silent monolith would mask (12-factor: the env is consumed or it errors).
//   - role ∈ declared set → Colocated = the role's own cells, Remote = every
//     other group's cells mapped to that group's endpoint (the form
//     celltransport.Resolve consumes).
//   - role ∉ declared set → fail-closed.
//
// ref: akka/akka cluster-sharding withRole — one artifact, a static role config
// selects which cells the node hosts.
func SpecForRole(groups []TopologyGroup, role string) (DeploymentTopologySpec, error) {
	if role == "" {
		if len(groups) >= 2 {
			return DeploymentTopologySpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				errMsgDeployTopoRoleRequired,
				errcode.WithInternal(
					errcode.InternalAttr("groupCount", len(groups)),
					errcode.InternalAttr("availableRoles", topoGroupRoles(groups))))
		}
		return DeploymentTopologySpec{}, nil // 0/1 group → all-colocated monolith
	}
	return deriveRoleSpec(groups, role)
}

// topoGroupRoles renders the declared role names (sorted, comma-joined) for
// diagnostic InternalAttrs — lets an operator see the valid GOCELL_CELL_ROLE
// values in the server log without consulting assembly.yaml.
func topoGroupRoles(groups []TopologyGroup) string {
	roles := make([]string, 0, len(groups))
	for _, g := range groups {
		roles = append(roles, g.Role)
	}
	sort.Strings(roles)
	return strings.Join(roles, ",")
}

// deriveRoleSpec builds the per-process spec for a named role: Colocated = the
// role's own cells; Remote = every other group's cells mapped to that group's
// endpoint. Fails closed if role is not a declared group. Extracted to keep
// SpecForRole within the cognitive-complexity budget. Output is sorted by cellID
// for determinism (newDeploymentTopology builds maps, so order is otherwise
// irrelevant — sorting only aids tests and diagnostics).
func deriveRoleSpec(groups []TopologyGroup, role string) (DeploymentTopologySpec, error) {
	var spec DeploymentTopologySpec
	found := false
	for _, g := range groups {
		if g.Role == role {
			found = true
			spec.Colocated = append([]string(nil), g.Cells...)
			// Project the SELECTED role's broker signal (not an OR across groups):
			// the gate fires per-process, and only this process's role matters.
			spec.RequiresBrokerForCrossProcessEvents = g.RequiresBrokerForCrossProcessEvents
			continue
		}
		for _, c := range g.Cells {
			spec.Remote = append(spec.Remote, RemoteCellEndpoint{CellID: c, Endpoint: g.Endpoint})
		}
	}
	if !found {
		return DeploymentTopologySpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			errMsgDeployTopoUnknownRole,
			errcode.WithInternal(
				errcode.InternalAttr("role", role),
				errcode.InternalAttr("availableRoles", topoGroupRoles(groups))),
			errcode.WithDetails(errcode.PublicString("role", role)))
	}
	sort.Strings(spec.Colocated)
	sort.Slice(spec.Remote, func(i, j int) bool { return spec.Remote[i].CellID < spec.Remote[j].CellID })
	return spec, nil
}

// DeploymentTopologySpec is the PLAIN, codegen-produced input describing the
// assembly's deployment placement (single-sourced from assembly.yaml topology,
// derived at the composition root via SpecForRole(generatedTopologyGroups(), role)).
// It carries no
// validation — phase0 seals+validates it into a DeploymentTopology. Empty spec
// => all cells co-located (zero-migration default).
type DeploymentTopologySpec struct {
	Colocated []string
	Remote    []RemoteCellEndpoint
	// RequiresBrokerForCrossProcessEvents is the codegen-derived per-process
	// signal projected from the selected role's TopologyGroup (see that field's
	// doc). True => this process participates in cross-process event pub/sub and
	// the phase0 broker-mandatory gate demands a real broker; false => sync-only
	// (or colocated) and the in-memory bus is permitted.
	RequiresBrokerForCrossProcessEvents bool
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
	// requiresBrokerForCrossProcessEvents is the sealed copy of the spec's
	// codegen-derived per-process broker signal (see DeploymentTopologySpec). It
	// is the sole input to the phase0 broker-mandatory gate; queried via
	// RequiresBrokerForCrossProcessEvents(). Zero value (all-colocated) = false.
	requiresBrokerForCrossProcessEvents bool
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
		explicit:                            true,
		colocated:                           colocated,
		remote:                              remote,
		requiresBrokerForCrossProcessEvents: spec.RequiresBrokerForCrossProcessEvents,
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
// (no explicit topology, all-colocated) returns false.
//
// This is the generic "is this a split deployment?" predicate, consumed by the
// split-mTLS gates (HasNonLoopbackRemoteCells / SharedNonLoopbackRemoteEndpoint
// build on the same remote set) and celltransport wiring. It is NOT the
// broker-mandatory trigger: since #2196 that gate keys off the precise
// codegen-derived RequiresBrokerForCrossProcessEvents signal, so a sync-only
// split (remote cells, no cross-process events) is no longer over-rejected.
func (t DeploymentTopology) HasRemoteCells() bool {
	return len(t.remote) > 0
}

// RequiresBrokerForCrossProcessEvents reports whether this process participates
// in cross-process event pub/sub and therefore needs a real broker (the
// in-memory EventBus cannot deliver events across processes). It is the precise
// codegen-derived signal (single-sourced from assembly.yaml topology +
// contractUsages, projected per-role by SpecForRole, sealed here), and the sole
// trigger of the phase0 broker-mandatory gate (validateSplitTopologyBroker).
// Zero value (all-colocated, or a sync-only split) returns false.
func (t DeploymentTopology) RequiresBrokerForCrossProcessEvents() bool {
	return t.requiresBrokerForCrossProcessEvents
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

// SharedNonLoopbackRemoteEndpoint returns a non-loopback endpoint shared by ≥2
// remote cells, those cell IDs (sorted), and found=true — or found=false when
// every non-loopback remote endpoint is unique. Deterministic: the
// lexicographically-smallest colliding endpoint is returned.
//
// This is the #2263 split-mTLS single-cell-per-process guard signal: mTLS binds
// ONE cell SPIFFE identity per process (the internal listener presents one cell
// cert), so a process serving multiple cells at the SAME mTLS endpoint cannot
// present a correct per-cell certificate — the cross-bind would fail for all but
// one. cellmodules/celltls.Resolve fails closed on this when TLS material is
// provisioned. Loopback endpoints (local multi-process dev) are exempt: they are
// plaintext-eligible and carry no per-cell mTLS identity. The full fix (a
// per-caller-cell identity resolver lifting this one-cell-per-process limit) is a
// follow-up; see ADR 202606171200-2263.
func (t DeploymentTopology) SharedNonLoopbackRemoteEndpoint() (endpoint string, cells []string, found bool) {
	byEndpoint := make(map[string][]string, len(t.remote))
	for cellID, ep := range t.remote {
		if netutil.IsLoopbackEndpoint(ep) {
			continue
		}
		byEndpoint[ep] = append(byEndpoint[ep], cellID)
	}
	collisions := make([]string, 0, len(byEndpoint))
	for ep, ids := range byEndpoint {
		if len(ids) >= 2 {
			collisions = append(collisions, ep)
		}
	}
	if len(collisions) == 0 {
		return "", nil, false
	}
	sort.Strings(collisions)
	ep := collisions[0]
	ids := append([]string(nil), byEndpoint[ep]...)
	sort.Strings(ids)
	return ep, ids, true
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
