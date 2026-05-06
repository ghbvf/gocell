// Package catalog provides the Backstage-style wire format and BuildDocument
// function for GoCell's devtools catalog endpoint.
//
// Wire types live here (not in kernel/metadata) so that downstream products
// can import kernel/metadata for the project model without being locked into
// this particular wire format.
//
// ref: backstage/backstage packages/catalog-model/src/entity/Entity.ts@master
// ref: backstage/backstage docs/features/software-catalog/well-known-relations.md@master
package catalog

import (
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/kernel/governance"
)

// Wire-format constants. Bumping either of these is a breaking change that
// requires a coordinated update of every consumer (gocell-web, ops dashboards,
// CI scripts).
const (
	SchemaVersionV1 = "v1"
	APIVersionV1    = "gocell.io/v1alpha1"
)

// AllKinds enumerates the entity kinds BuildDocument can emit.
var AllKinds = []string{"Actor", "Assembly", "Cell", "Contract", "Journey", "Slice"}

// AllLayers enumerates the layers used by entityLayer + tools/depgraph nodes.
var AllLayers = []string{
	"adapters", "actors", "assemblies", "cells", "cmd", "contracts",
	"examples", "generated", "journeys", "kernel", "pkg", "root",
	"runtime", "stdlib", "tests", "thirdparty", "tools", "unknown",
}

// IncludeOptions selects which optional Document blocks BuildDocument will
// populate. Zero value = nothing optional included. Use AllIncluded() for
// full snapshots (CLI default).
type IncludeOptions struct {
	Relations   bool
	StatusBoard bool
	CellDeps    bool
	PackageDeps bool
}

// AllIncluded returns an IncludeOptions with every flag true.
// CLI default; HTTP default when no ?include= query parameter is provided.
func AllIncluded() IncludeOptions {
	return IncludeOptions{
		Relations:   true,
		StatusBoard: true,
		CellDeps:    true,
		PackageDeps: true,
	}
}

// Filter is the projection applied to a ProjectMeta when building a Document.
// Empty slices mean "no filter on this dimension" (all entities pass). Cells,
// when non-empty, switches to focus mode: only the listed cells + their
// first-order neighbors (slices belongsTo, L0 deps, contract owners/clients)
// appear in the output.
type Filter struct {
	Kinds   []string
	Layers  []string
	Cells   []string
	Include IncludeOptions
}

// FilterEcho is the applied filter as it appears in Document.Query — i.e. with
// defaults already resolved (Include expanded into a sorted []string slice for
// wire stability). Allows clients to confirm which filters the server actually
// applied without parsing query strings themselves.
type FilterEcho struct {
	Kinds  []string `json:"kinds,omitempty"  yaml:"kinds,omitempty"`
	Layers []string `json:"layers,omitempty" yaml:"layers,omitempty"`
	Cells  []string `json:"cells,omitempty"  yaml:"cells,omitempty"`
	// Include is a sorted subset of AllIncludeTokens; omitted when empty.
	Include []string `json:"include,omitempty" yaml:"include,omitempty"`
}

// Document is the top-level wire envelope. Backstage-style five-field root +
// schemaVersion / generatedAt / root / query echo. Marshaled deterministically
// (Entities sorted by (kind, name); each Entity's Relations sorted by
// (type, targetRef)) so two builds of the same project byte-equal.
type Document struct {
	SchemaVersion string             `json:"schemaVersion" yaml:"schemaVersion"`
	APIVersion    string             `json:"apiVersion"    yaml:"apiVersion"`
	GeneratedAt   string             `json:"generatedAt"   yaml:"generatedAt"`
	Root          string             `json:"root"          yaml:"root"`
	Query         FilterEcho         `json:"query"         yaml:"query"`
	Entities      []Entity           `json:"entities,omitempty"     yaml:"entities,omitempty"`
	StatusBoard   []StatusBoardEntry `json:"statusBoard,omitempty"  yaml:"statusBoard,omitempty"`
	Dependencies  *Dependencies      `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
}

// StatusBoardEntry is a single row in the status board — one per journey.
// Risk and Blocker are redacted (cleared) for draft/planned entries before
// wire serialization so internal planning narratives do not leak.
type StatusBoardEntry struct {
	JourneyID string `json:"journeyId" yaml:"journeyId"`
	State     string `json:"state"     yaml:"state"`
	Risk      string `json:"risk,omitempty"    yaml:"risk,omitempty"`
	Blocker   string `json:"blocker,omitempty" yaml:"blocker,omitempty"`
	UpdatedAt string `json:"updatedAt"         yaml:"updatedAt"`
}

// Entity is the per-resource wrapper — one per Cell, Slice, Contract, Journey,
// Assembly, or Actor. Spec carries a kind-specific typed struct (CellSpec,
// SliceSpec, ...). Encoded as JSON object regardless of the underlying type.
type Entity struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Kind       string         `json:"kind"       yaml:"kind"` // Cell|Slice|Contract|Journey|Assembly|Actor
	Metadata   EntityMetadata `json:"metadata"   yaml:"metadata"`
	Spec       any            `json:"spec"       yaml:"spec"`
	Relations  []Relation     `json:"relations,omitempty" yaml:"relations,omitempty"`
}

// EntityMetadata holds Backstage-style metadata fields. Name is the technical
// identifier; UID is the catalog-stable canonical reference (kind/name).
type EntityMetadata struct {
	Name        string            `json:"name"            yaml:"name"`
	Namespace   string            `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	UID         string            `json:"uid"             yaml:"uid"`
	File        string            `json:"file,omitempty"  yaml:"file,omitempty"`
	Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// Relation declares a directed link between two entities. TargetRef format:
// "{kind}/{name}" (lowercased kind, e.g. "slice/configcore.flagwrite").
type Relation struct {
	Type      string `json:"type"      yaml:"type"`
	TargetRef string `json:"targetRef" yaml:"targetRef"`
}

// Dependencies aggregates the optional dep-graph blocks. Either field may be
// nil; serialization elides nil branches via omitempty.
type Dependencies struct {
	Cells    *CellDepGraph    `json:"cells,omitempty"    yaml:"cells,omitempty"`
	Packages *PackageDepsView `json:"packages,omitempty" yaml:"packages,omitempty"`
}

// CellDepGraph is the cell-level (cell.yaml dependencies field) view. Built by
// the caller from kernel/governance.DependencyChecker.Graph(). Nodes/Edges
// sorted deterministically.
//
// BuiltAt records when this graph was last constructed (RFC3339 UTC). HTTP
// clients can use this to detect stale graphs — the field does not change
// between requests because the graph is built once at bootstrap time.
type CellDepGraph struct {
	Nodes   []string   `json:"nodes"             yaml:"nodes"`
	Edges   []CellEdge `json:"edges"             yaml:"edges"`
	BuiltAt string     `json:"builtAt,omitempty" yaml:"builtAt,omitempty"`
}

// CellEdge is a directed dependency between two cells.
type CellEdge struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to"   yaml:"to"`
}

// PackageDepsView is the package-level (Go import) view. Consumers determine
// state by checking: Graph != nil (ready) or Error != "" (error). Graph is the
// typed dep graph from tools/depgraph.Load — kept as a *kerneldepgraph.Graph
// pointer so wire shape stays canonical (kernel/depgraph owns its own
// MarshalJSON for determinism).
type PackageDepsView struct {
	Graph *kerneldepgraph.Graph `json:"graph,omitempty" yaml:"graph,omitempty"`
	Error string                `json:"error,omitempty" yaml:"error,omitempty"`
}

// CellSpec is Document.Entities[Kind=="Cell"].Spec.
type CellSpec struct {
	Type             string          `json:"type"             yaml:"type"`
	ConsistencyLevel string          `json:"consistencyLevel" yaml:"consistencyLevel"`
	DurabilityMode   string          `json:"durabilityMode,omitempty" yaml:"durabilityMode,omitempty"`
	Owner            CellSpecOwner   `json:"owner"            yaml:"owner"`
	Schema           CellSpecSchema  `json:"schema"           yaml:"schema"`
	VerifySmoke      []string        `json:"verifySmoke,omitempty"    yaml:"verifySmoke,omitempty"`
	L0Dependencies   []CellSpecL0Dep `json:"l0Dependencies,omitempty" yaml:"l0Dependencies,omitempty"`
	Slices           []string        `json:"slices,omitempty"         yaml:"slices,omitempty"` // canonical "cell.slice" IDs
}

// CellSpecOwner mirrors OwnerMeta on the wire (camelCase tags).
type CellSpecOwner struct {
	Team string `json:"team" yaml:"team"`
	Role string `json:"role" yaml:"role"`
}

// CellSpecSchema mirrors SchemaMeta on the wire.
type CellSpecSchema struct {
	Primary string `json:"primary" yaml:"primary"`
}

// CellSpecL0Dep mirrors L0DepMeta on the wire.
type CellSpecL0Dep struct {
	Cell   string `json:"cell"             yaml:"cell"`
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// SliceSpec is Document.Entities[Kind=="Slice"].Spec.
type SliceSpec struct {
	BelongsToCell    string                   `json:"belongsToCell"   yaml:"belongsToCell"`
	ConsistencyLevel string                   `json:"consistencyLevel" yaml:"consistencyLevel"`
	ContractUsages   []SliceSpecContractUsage `json:"contractUsages,omitempty" yaml:"contractUsages,omitempty"`
	VerifyUnit       []string                 `json:"verifyUnit,omitempty"     yaml:"verifyUnit,omitempty"`
	VerifyContract   []string                 `json:"verifyContract,omitempty" yaml:"verifyContract,omitempty"`
	AllowedFiles     []string                 `json:"allowedFiles,omitempty"   yaml:"allowedFiles,omitempty"`
}

// SliceSpecContractUsage mirrors ContractUsageMeta on the wire.
type SliceSpecContractUsage struct {
	Contract string `json:"contract" yaml:"contract"`
	Role     string `json:"role"     yaml:"role"`
}

// ContractSpec is Document.Entities[Kind=="Contract"].Spec.
//
// Note: this name shadows wrapper.ContractSpec in some import contexts;
// callers in cmd/ and runtime/ should import this package as `catalog` so the
// disambiguation reads naturally (`catalog.ContractSpec` vs
// `wrapper.ContractSpec`).
type ContractSpec struct {
	Kind              string   `json:"kind"               yaml:"kind"`
	OwnerCell         string   `json:"ownerCell,omitempty" yaml:"ownerCell,omitempty"`
	ConsistencyLevel  string   `json:"consistencyLevel,omitempty" yaml:"consistencyLevel,omitempty"`
	Lifecycle         string   `json:"lifecycle,omitempty" yaml:"lifecycle,omitempty"`
	Triggers          []string `json:"triggers,omitempty" yaml:"triggers,omitempty"`
	Replayable        bool     `json:"replayable,omitempty"         yaml:"replayable,omitempty"`
	IdempotencyKey    string   `json:"idempotencyKey,omitempty"     yaml:"idempotencyKey,omitempty"`
	DeliverySemantics string   `json:"deliverySemantics,omitempty"  yaml:"deliverySemantics,omitempty"`
	Description       string   `json:"description,omitempty"        yaml:"description,omitempty"`
	DeprecatedAt      string   `json:"deprecatedAt,omitempty"       yaml:"deprecatedAt,omitempty"`
}

// JourneySpec is Document.Entities[Kind=="Journey"].Spec.
type JourneySpec struct {
	Goal         string            `json:"goal,omitempty"      yaml:"goal,omitempty"`
	Lifecycle    string            `json:"lifecycle,omitempty" yaml:"lifecycle,omitempty"`
	Owner        CellSpecOwner     `json:"owner"               yaml:"owner"`
	Cells        []string          `json:"cells,omitempty"     yaml:"cells,omitempty"`
	Contracts    []string          `json:"contracts,omitempty" yaml:"contracts,omitempty"`
	PassCriteria []JourneyPassCrit `json:"passCriteria,omitempty" yaml:"passCriteria,omitempty"`
}

// JourneyPassCrit mirrors PassCriterionMeta on the wire.
type JourneyPassCrit struct {
	Text     string `json:"text"           yaml:"text"`
	Mode     string `json:"mode,omitempty" yaml:"mode,omitempty"`
	CheckRef string `json:"checkRef,omitempty" yaml:"checkRef,omitempty"`
}

// AssemblySpec is Document.Entities[Kind=="Assembly"].Spec.
type AssemblySpec struct {
	Cells               []string          `json:"cells,omitempty"               yaml:"cells,omitempty"`
	Owner               CellSpecOwner     `json:"owner"                         yaml:"owner"`
	MaxConsistencyLevel string            `json:"maxConsistencyLevel,omitempty" yaml:"maxConsistencyLevel,omitempty"`
	Build               AssemblySpecBuild `json:"build"                         yaml:"build"`
}

// AssemblySpecBuild mirrors AssemblyBuildMeta on the wire.
type AssemblySpecBuild struct {
	Entrypoint     string `json:"entrypoint,omitempty"     yaml:"entrypoint,omitempty"`
	Binary         string `json:"binary,omitempty"         yaml:"binary,omitempty"`
	DeployTemplate string `json:"deployTemplate,omitempty" yaml:"deployTemplate,omitempty"`
}

// ActorSpec is Document.Entities[Kind=="Actor"].Spec.
type ActorSpec struct {
	MaxConsistencyLevel string `json:"maxConsistencyLevel,omitempty" yaml:"maxConsistencyLevel,omitempty"`
}

// NewCellDepGraph constructs a CellDepGraph from a governance.Graph snapshot.
// Both HTTP (bootstrap.phase5InitDevtoolsHandler) and CLI (cmd/gocell export
// catalog) call this so the wire shape and field-population semantics stay in
// lock-step. BuiltAt is set from clk so consumers can detect stale graphs.
func NewCellDepGraph(g governance.Graph, clk clock.Clock) *CellDepGraph {
	edges := make([]CellEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, CellEdge{From: e.From, To: e.To})
	}
	return &CellDepGraph{
		Nodes:   append([]string(nil), g.Nodes...),
		Edges:   edges,
		BuiltAt: clk.Now().UTC().Format(time.RFC3339),
	}
}
