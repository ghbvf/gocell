// Package metadata · export.go: catalog wire format + BuildDocument.
//
// This file declares the Backstage-Catalog-inspired wire format used by:
//   - cmd/gocell/app/export.go (CLI: `gocell export catalog`)
//   - runtime/http/devtools/catalog.go (HTTP: GET /api/v1/devtools/catalog)
//
// Both surfaces share BuildDocument as the single core function. Wire form is
// frozen by SchemaVersion="v1" + APIVersion="gocell.io/v1alpha1"; new fields
// only ever added with `omitempty`, never removed/renamed within v1.
//
// ref: backstage/backstage packages/catalog-model/src/entity/Entity.ts@master
// ref: backstage/backstage docs/features/software-catalog/well-known-relations.md@master
package metadata

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
)

// Wire-format constants. Bumping either of these is a breaking change that
// requires a coordinated update of every consumer (gocell-web, ops dashboards,
// CI scripts).
const (
	SchemaVersionV1 = "v1"
	APIVersionV1    = "gocell.io/v1alpha1"
)

// IncludeMask is a bitmask selecting which optional Document blocks the caller
// wants populated. Zero value = nothing optional included. Use IncludeAll for
// full snapshots (CLI default).
type IncludeMask uint8

const (
	IncludeRelations IncludeMask = 1 << iota
	IncludeStatusBoard
	IncludeCellDeps
	IncludePackageDeps
)

// IncludeAll selects every optional block. CLI default; HTTP default when no
// `?include=` query parameter is provided.
const IncludeAll = IncludeRelations | IncludeStatusBoard | IncludeCellDeps | IncludePackageDeps

// AllKinds enumerates the entity kinds BuildDocument can emit. Consumers
// that need a whitelist (HTTP query validation, CLI flag validation) should
// reference this slice as the single source of truth.
var AllKinds = []string{"Actor", "Assembly", "Cell", "Contract", "Journey", "Slice"}

// AllLayers enumerates the layers used by entityLayer + tools/depgraph nodes.
// Consumers that need a whitelist should reference this slice.
var AllLayers = []string{
	"adapters", "actors", "assemblies", "cells", "cmd", "contracts",
	"examples", "generated", "journeys", "kernel", "pkg", "root",
	"runtime", "stdlib", "tests", "thirdparty", "tools", "unknown",
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
	Include IncludeMask
}

// FilterEcho is the applied filter as it appears in Document.Query — i.e. with
// defaults already resolved (Include expanded into a sorted []string slice for
// wire stability). Allows clients to confirm which filters the server actually
// applied without parsing query strings themselves.
type FilterEcho struct {
	Kinds   []string `json:"kinds,omitempty"  yaml:"kinds,omitempty"`
	Layers  []string `json:"layers,omitempty" yaml:"layers,omitempty"`
	Cells   []string `json:"cells,omitempty"  yaml:"cells,omitempty"`
	Include []string `json:"include"          yaml:"include"` // sorted: ["cellDeps","packageDeps","relations","statusBoard"] subset
}

// ExportOptions configures BuildDocument. All fields besides Now and Filter
// are optional; nil values produce a Document without the corresponding block.
type ExportOptions struct {
	// Now is the timestamp embedded as Document.GeneratedAt. Required.
	Now time.Time
	// Root is the project root path echoed back in Document.Root. Optional.
	Root string
	// Filter projects entities and dependencies. Zero value = full snapshot.
	Filter Filter
	// CellDeps, when non-nil, populates Document.Dependencies.Cells. Caller
	// constructs by invoking governance.DependencyChecker.Graph() and
	// translating to *CellDepGraph (kernel/metadata cannot import governance
	// to avoid the metadata→governance→metadata cycle).
	CellDeps *CellDepGraph
	// Packages, when non-nil, populates Document.Dependencies.Packages. The
	// pointer carries its own status field (loading|ready|error) so HTTP lazy
	// loading and CLI synchronous loading share one shape.
	Packages *PackageDepsView
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

// PackageDepsView is the package-level (Go import) view. Status reflects the
// loader state: HTTP handler builds Document immediately at request time even
// if the background depgraph load is still in progress, returning Status=
// "loading". CLI runs Load synchronously so Status is always "ready" or
// "error". Graph is the typed dep graph from tools/depgraph.Load — kept as a
// *kerneldepgraph.Graph pointer rather than a copied wire struct so wire
// shape stays canonical (kernel/depgraph owns its own MarshalJSON for
// determinism).
type PackageDepsView struct {
	Status string                `json:"status" yaml:"status"`
	Graph  *kerneldepgraph.Graph `json:"graph,omitempty" yaml:"graph,omitempty"`
	Error  string                `json:"error,omitempty" yaml:"error,omitempty"`
}

// CellSpec is Document.Entities[Kind=="Cell"].Spec. One typed struct per kind
// keeps the envelope honest — `any` is the discriminator transport, but the
// concrete struct under it is fully typed and JSON/YAML-round-trippable.
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
// callers in cmd/ and runtime/ import this package as `metadata` so the
// disambiguation reads naturally (`metadata.ContractSpec` vs
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
	Cells []string          `json:"cells,omitempty" yaml:"cells,omitempty"`
	Build AssemblySpecBuild `json:"build"          yaml:"build"`
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

// BuildDocument projects pm into a Document according to opts. Pure function:
// no I/O, no time.Now() calls (Now is supplied via opts), no global state
// access. Suitable for table-driven tests with fixture ProjectMeta values.
//
// Returns an error only when opts is structurally invalid (e.g. opts.Now is
// the zero time). All filter combinations produce a valid (possibly empty)
// Document; an empty result is not an error.
func BuildDocument(pm *ProjectMeta, opts ExportOptions) (Document, error) {
	if opts.Now.IsZero() {
		return Document{}, fmt.Errorf("metadata.BuildDocument: opts.Now must not be zero")
	}
	if pm == nil {
		pm = &ProjectMeta{}
	}

	entities := buildEntities(pm, opts.Filter)

	// Expand the cell focus set before filtering so first-order neighbors
	// (L0 deps, contract clients, contract owners) are included in the output.
	filter := opts.Filter
	if len(filter.Cells) > 0 {
		filter.Cells = expandCellFocusSet(pm, filter.Cells)
	}

	entities = applyFilter(entities, filter)
	sortEntities(entities)

	doc := Document{
		SchemaVersion: SchemaVersionV1,
		APIVersion:    APIVersionV1,
		GeneratedAt:   opts.Now.UTC().Format(time.RFC3339),
		Root:          opts.Root,
		Query:         buildFilterEcho(opts.Filter),
		Entities:      entities,
	}

	if filter.Include&IncludeStatusBoard != 0 {
		doc.StatusBoard = redactStatusBoard(pm.StatusBoard)
	}

	// Pass the expanded filter for dependency graph filtering.
	optsWithExpandedFilter := opts
	optsWithExpandedFilter.Filter = filter
	doc.Dependencies = buildDependencies(optsWithExpandedFilter, entities)

	return doc, nil
}

// redactStatusBoard returns a copy of entries with risk and blocker fields
// cleared for entries in speculative states (draft or planned). Entries in
// operational states (todo/doing/blocked/ready) are returned unchanged.
// This prevents internal planning narratives from leaking into publicly
// embedded consumers (e.g. gocell-web bundle) while still exposing
// journey presence and delivery state.
func redactStatusBoard(entries []StatusBoardEntry) []StatusBoardEntry {
	out := make([]StatusBoardEntry, len(entries))
	for i, e := range entries {
		out[i] = e
		if e.State == "draft" || e.State == "planned" {
			out[i].Risk = ""
			out[i].Blocker = ""
		}
	}
	return out
}

// buildEntities collects all entities from pm, applying relations based on mask.
func buildEntities(pm *ProjectMeta, filter Filter) []Entity {
	var entities []Entity

	for _, c := range pm.Cells {
		entities = append(entities, buildCellEntity(c, filter.Include))
	}
	for key, s := range pm.Slices {
		_ = key
		entities = append(entities, buildSliceEntity(s, filter.Include))
	}
	for _, c := range pm.Contracts {
		entities = append(entities, buildContractEntity(c, filter.Include))
	}
	for _, j := range pm.Journeys {
		entities = append(entities, buildJourneyEntity(j, filter.Include))
	}
	for _, a := range pm.Assemblies {
		entities = append(entities, buildAssemblyEntity(a, filter.Include))
	}
	for _, a := range pm.Actors {
		entities = append(entities, buildActorEntity(a, filter.Include))
	}

	return entities
}

// buildCellEntity converts CellMeta to an Entity.
func buildCellEntity(c *CellMeta, mask IncludeMask) Entity {
	l0Deps := make([]CellSpecL0Dep, 0, len(c.L0Dependencies))
	for _, d := range c.L0Dependencies {
		l0Deps = append(l0Deps, CellSpecL0Dep(d))
	}

	spec := CellSpec{
		Type:             c.Type,
		ConsistencyLevel: c.ConsistencyLevel,
		DurabilityMode:   c.DurabilityMode,
		Owner:            CellSpecOwner{Team: c.Owner.Team, Role: c.Owner.Role},
		Schema:           CellSpecSchema{Primary: c.Schema.Primary},
		VerifySmoke:      c.Verify.Smoke,
		L0Dependencies:   l0Deps,
	}

	var rels []Relation
	if mask&IncludeRelations != 0 {
		for _, dep := range c.L0Dependencies {
			rels = append(rels, Relation{Type: "dependsOn", TargetRef: "cell/" + dep.Cell})
		}
		sortRelations(rels)
	}

	return Entity{
		APIVersion: APIVersionV1,
		Kind:       "Cell",
		Metadata: EntityMetadata{
			Name: c.ID,
			UID:  "cell/" + c.ID,
			File: c.File,
		},
		Spec:      spec,
		Relations: rels,
	}
}

// buildSliceEntity converts SliceMeta to an Entity.
func buildSliceEntity(s *SliceMeta, mask IncludeMask) Entity {
	usages := make([]SliceSpecContractUsage, 0, len(s.ContractUsages))
	for _, u := range s.ContractUsages {
		usages = append(usages, SliceSpecContractUsage(u))
	}

	spec := SliceSpec{
		BelongsToCell:    s.BelongsToCell,
		ConsistencyLevel: s.ConsistencyLevel,
		ContractUsages:   usages,
		VerifyUnit:       s.Verify.Unit,
		VerifyContract:   s.Verify.Contract,
		AllowedFiles:     s.AllowedFiles,
	}

	var rels []Relation
	if mask&IncludeRelations != 0 {
		rels = append(rels, Relation{Type: "partOf", TargetRef: "cell/" + s.BelongsToCell})
		for _, u := range s.ContractUsages {
			rels = append(rels, Relation{Type: "uses", TargetRef: "contract/" + u.Contract})
		}
		sortRelations(rels)
	}

	return Entity{
		APIVersion: APIVersionV1,
		Kind:       "Slice",
		Metadata: EntityMetadata{
			Name: s.ID,
			UID:  "slice/" + s.ID,
			File: s.File,
		},
		Spec:      spec,
		Relations: rels,
	}
}

// buildContractEntity converts ContractMeta to an Entity.
func buildContractEntity(c *ContractMeta, mask IncludeMask) Entity {
	replayable := false
	if c.Replayable != nil {
		replayable = *c.Replayable
	}

	spec := ContractSpec{
		Kind:              c.Kind,
		OwnerCell:         c.OwnerCell,
		ConsistencyLevel:  c.ConsistencyLevel,
		Lifecycle:         c.Lifecycle,
		Triggers:          c.Triggers,
		Replayable:        replayable,
		IdempotencyKey:    c.IdempotencyKey,
		DeliverySemantics: c.DeliverySemantics,
		Description:       c.Description,
		DeprecatedAt:      c.DeprecatedAt,
	}

	var rels []Relation
	if mask&IncludeRelations != 0 && c.OwnerCell != "" {
		rels = append(rels, Relation{Type: "ownedBy", TargetRef: "cell/" + c.OwnerCell})
		sortRelations(rels)
	}

	return Entity{
		APIVersion: APIVersionV1,
		Kind:       "Contract",
		Metadata: EntityMetadata{
			Name: c.ID,
			UID:  "contract/" + c.ID,
			File: c.File,
		},
		Spec:      spec,
		Relations: rels,
	}
}

// buildJourneyEntity converts JourneyMeta to an Entity.
func buildJourneyEntity(j *JourneyMeta, mask IncludeMask) Entity {
	crit := make([]JourneyPassCrit, 0, len(j.PassCriteria))
	for _, p := range j.PassCriteria {
		crit = append(crit, JourneyPassCrit(p))
	}

	spec := JourneySpec{
		Goal:         j.Goal,
		Lifecycle:    j.Lifecycle,
		Owner:        CellSpecOwner{Team: j.Owner.Team, Role: j.Owner.Role},
		Cells:        j.Cells,
		Contracts:    j.Contracts,
		PassCriteria: crit,
	}

	var rels []Relation
	if mask&IncludeRelations != 0 {
		for _, cellID := range j.Cells {
			rels = append(rels, Relation{Type: "covers", TargetRef: "cell/" + cellID})
		}
		sortRelations(rels)
	}

	return Entity{
		APIVersion: APIVersionV1,
		Kind:       "Journey",
		Metadata: EntityMetadata{
			Name: j.ID,
			UID:  "journey/" + j.ID,
			File: j.File,
		},
		Spec:      spec,
		Relations: rels,
	}
}

// buildAssemblyEntity converts AssemblyMeta to an Entity.
func buildAssemblyEntity(a *AssemblyMeta, mask IncludeMask) Entity {
	spec := AssemblySpec{
		Cells: a.Cells,
		Build: AssemblySpecBuild{
			Entrypoint:     a.Build.Entrypoint,
			Binary:         a.Build.Binary,
			DeployTemplate: a.Build.DeployTemplate,
		},
	}

	var rels []Relation
	if mask&IncludeRelations != 0 {
		for _, cellID := range a.Cells {
			rels = append(rels, Relation{Type: "contains", TargetRef: "cell/" + cellID})
		}
		sortRelations(rels)
	}

	return Entity{
		APIVersion: APIVersionV1,
		Kind:       "Assembly",
		Metadata: EntityMetadata{
			Name: a.ID,
			UID:  "assembly/" + a.ID,
			File: a.File,
		},
		Spec:      spec,
		Relations: rels,
	}
}

// buildActorEntity converts ActorMeta to an Entity.
func buildActorEntity(a ActorMeta, _ IncludeMask) Entity {
	spec := ActorSpec{
		MaxConsistencyLevel: a.MaxConsistencyLevel,
	}
	return Entity{
		APIVersion: APIVersionV1,
		Kind:       "Actor",
		Metadata: EntityMetadata{
			Name: a.ID,
			UID:  "actor/" + a.ID,
		},
		Spec: spec,
	}
}

// expandCellFocusSet computes the expanded cell set for focus-mode filtering.
// Starting from the seed cells in focus, it adds:
//   - L0 dependencies declared in each focus cell's cell.yaml
//   - Cells that own contracts consumed by slices of focus cells
//   - Cells that are clients of contracts owned by focus cells
//
// Returns a deduplicated, sorted slice so the result is stable.
func expandCellFocusSet(pm *ProjectMeta, focus []string) []string {
	expanded := make(map[string]bool, len(focus))
	for _, c := range focus {
		expanded[c] = true
	}

	addL0Deps(pm, expanded, focus)

	contractOwner, contractClients := buildContractMaps(pm)

	addContractClients(pm, expanded, contractOwner, contractClients)
	addContractOwners(pm, expanded, contractOwner)

	result := make([]string, 0, len(expanded))
	for c := range expanded {
		result = append(result, c)
	}
	sort.Strings(result)
	return result
}

// addL0Deps adds L0 dependency cell IDs to expanded for each focus cell.
func addL0Deps(pm *ProjectMeta, expanded map[string]bool, focus []string) {
	for _, f := range focus {
		cell := pm.Cells[f]
		if cell == nil {
			continue
		}
		for _, dep := range cell.L0Dependencies {
			expanded[dep.Cell] = true
		}
	}
}

// buildContractMaps returns two maps: contractID→ownerCell and
// contractID→[]consumerCells derived from endpoint declarations.
func buildContractMaps(pm *ProjectMeta) (ownerMap map[string]string, clientsMap map[string][]string) {
	ownerMap = make(map[string]string, len(pm.Contracts))
	clientsMap = make(map[string][]string, len(pm.Contracts))
	for _, c := range pm.Contracts {
		if c == nil {
			continue
		}
		ownerMap[c.ID] = c.OwnerCell
		clientsMap[c.ID] = contractConsumers(c)
	}
	return
}

// contractConsumers returns the endpoint consumer cell IDs for a contract.
func contractConsumers(c *ContractMeta) []string {
	switch c.Kind {
	case "http":
		return c.Endpoints.Clients
	case "event":
		return c.Endpoints.Subscribers
	case "command":
		return c.Endpoints.Invokers
	case "projection":
		return c.Endpoints.Readers
	default:
		return nil
	}
}

// addContractClients adds cells that consume contracts owned by focus cells.
func addContractClients(pm *ProjectMeta, expanded map[string]bool, ownerMap map[string]string, clientsMap map[string][]string) {
	for contractID, ownerCell := range ownerMap {
		if !expanded[ownerCell] {
			continue
		}
		for _, client := range clientsMap[contractID] {
			if pm.Cells[client] != nil {
				expanded[client] = true
			}
		}
	}
}

// addContractOwners adds owner cells of contracts consumed by slices of focus cells.
func addContractOwners(pm *ProjectMeta, expanded map[string]bool, ownerMap map[string]string) {
	for _, s := range pm.Slices {
		if s == nil || !expanded[s.BelongsToCell] {
			continue
		}
		for _, u := range s.ContractUsages {
			if owner, ok := ownerMap[u.Contract]; ok && owner != "" && pm.Cells[owner] != nil {
				expanded[owner] = true
			}
		}
	}
}

// applyFilter applies kind, layer, and cell focus filters to entities.
func applyFilter(entities []Entity, filter Filter) []Entity {
	if len(filter.Kinds) == 0 && len(filter.Layers) == 0 && len(filter.Cells) == 0 {
		return entities
	}

	kindSet := toStringSet(filter.Kinds)
	layerSet := toStringSet(filter.Layers)
	cellSet := toStringSet(filter.Cells)
	consumedContracts := buildConsumedContracts(entities, cellSet)

	var out []Entity
	for _, e := range entities {
		if len(kindSet) > 0 && !kindSet[e.Kind] {
			continue
		}
		if len(layerSet) > 0 && !layerSet[entityLayer(e.Kind)] {
			continue
		}
		if len(cellSet) > 0 && !cellFocusMatch(e, cellSet, consumedContracts) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// toStringSet converts a slice of strings to a map[string]bool for O(1) lookup.
func toStringSet(vals []string) map[string]bool {
	if len(vals) == 0 {
		return nil
	}
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// buildConsumedContracts scans slice entities and returns the set of contract
// IDs referenced via contractUsages by slices belonging to any cell in cellSet.
func buildConsumedContracts(entities []Entity, cellSet map[string]bool) map[string]bool {
	if len(cellSet) == 0 {
		return nil
	}
	consumed := make(map[string]bool)
	for _, e := range entities {
		if e.Kind != "Slice" {
			continue
		}
		spec, ok := e.Spec.(SliceSpec)
		if !ok || !cellSet[spec.BelongsToCell] {
			continue
		}
		for _, u := range spec.ContractUsages {
			consumed[u.Contract] = true
		}
	}
	return consumed
}

// cellFocusMatch returns true if the entity should be included when filtering
// by cell focus. An entity passes when:
//   - Kind==Cell and the cell is in cellSet
//   - Kind==Slice and its BelongsToCell is in cellSet
//   - Kind==Contract and its OwnerCell is in cellSet, OR any focus-cell slice
//     references it via contractUsages (consumer-side match, pre-computed in
//     consumedContracts by applyFilter)
func cellFocusMatch(e Entity, cellSet map[string]bool, consumedContracts map[string]bool) bool {
	if e.Kind == "Cell" && cellSet[e.Metadata.Name] {
		return true
	}
	if e.Kind == "Slice" {
		spec, ok := e.Spec.(SliceSpec)
		if ok && cellSet[spec.BelongsToCell] {
			return true
		}
	}
	if e.Kind == "Contract" {
		spec, ok := e.Spec.(ContractSpec)
		if ok && cellSet[spec.OwnerCell] {
			return true
		}
		// Also include contracts consumed by any focus-cell slice.
		if consumedContracts[e.Metadata.Name] {
			return true
		}
	}
	return false
}

// entityLayer maps a Kind string to its architectural layer name.
func entityLayer(kind string) string {
	switch kind {
	case "Cell", "Slice":
		return "cells"
	case "Contract":
		return "contracts"
	case "Journey":
		return "journeys"
	case "Assembly":
		return "assemblies"
	case "Actor":
		return "actors"
	default:
		return ""
	}
}

// sortEntities sorts in-place by (Kind, Metadata.Name).
func sortEntities(entities []Entity) {
	sort.Slice(entities, func(i, j int) bool {
		ki := entities[i].Kind + "/" + entities[i].Metadata.Name
		kj := entities[j].Kind + "/" + entities[j].Metadata.Name
		return ki < kj
	})
}

// sortRelations sorts in-place by (Type, TargetRef).
func sortRelations(rels []Relation) {
	sort.Slice(rels, func(i, j int) bool {
		ki := rels[i].Type + "/" + rels[i].TargetRef
		kj := rels[j].Type + "/" + rels[j].TargetRef
		return ki < kj
	})
}

// buildFilterEcho converts the Filter to its wire echo form.
func buildFilterEcho(f Filter) FilterEcho {
	include := buildIncludeEcho(f.Include)
	return FilterEcho{
		Kinds:   f.Kinds,
		Layers:  f.Layers,
		Cells:   f.Cells,
		Include: include,
	}
}

// buildIncludeEcho converts IncludeMask to a sorted []string of active flag names.
func buildIncludeEcho(mask IncludeMask) []string {
	// Define the canonical bit-to-name mapping in stable order.
	type bitName struct {
		bit  IncludeMask
		name string
	}
	bits := []bitName{
		{IncludeCellDeps, "cellDeps"},
		{IncludePackageDeps, "packageDeps"},
		{IncludeRelations, "relations"},
		{IncludeStatusBoard, "statusBoard"},
	}
	var names []string
	for _, b := range bits {
		if mask&b.bit != 0 {
			names = append(names, b.name)
		}
	}
	sort.Strings(names)
	return names
}

// buildDependencies builds the Dependencies block from opts, applying the same
// filter used for entities so focused views stay consistent.
//
// entities is the already-filtered entity list; it is used to derive the
// allowed cell set for CellDepGraph filtering (focus + first-order neighbors
// that survived the entity filter).
func buildDependencies(opts ExportOptions, entities []Entity) *Dependencies {
	var deps Dependencies
	hasDeps := false

	if opts.Filter.Include&IncludeCellDeps != 0 && opts.CellDeps != nil {
		filtered := filterCellDepGraph(opts.CellDeps, opts.Filter, entities)
		deps.Cells = filtered
		hasDeps = true
	}
	if opts.Filter.Include&IncludePackageDeps != 0 && opts.Packages != nil {
		filtered := filterPackageDepsView(opts.Packages, opts.Filter)
		deps.Packages = filtered
		hasDeps = true
	}

	if !hasDeps {
		return nil
	}
	return &deps
}

// filterCellDepGraph returns a filtered CellDepGraph. When filter.Cells is
// non-empty, only nodes/edges for cells present in the filtered entities are
// included. When filter.Cells is empty, the original graph is returned as-is.
func filterCellDepGraph(g *CellDepGraph, filter Filter, entities []Entity) *CellDepGraph {
	if len(filter.Cells) == 0 {
		return g
	}
	// Build allowed set from entities that survived the filter (Kind==Cell).
	allowed := make(map[string]bool)
	for _, e := range entities {
		if e.Kind == "Cell" {
			allowed[e.Metadata.Name] = true
		}
	}
	if len(allowed) == 0 {
		return &CellDepGraph{Nodes: []string{}, Edges: []CellEdge{}}
	}

	var nodes []string
	for _, n := range g.Nodes {
		if allowed[n] {
			nodes = append(nodes, n)
		}
	}
	var edges []CellEdge
	for _, e := range g.Edges {
		if allowed[e.From] && allowed[e.To] {
			edges = append(edges, e)
		}
	}
	if nodes == nil {
		nodes = []string{}
	}
	if edges == nil {
		edges = []CellEdge{}
	}
	return &CellDepGraph{
		Nodes:   nodes,
		Edges:   edges,
		BuiltAt: g.BuiltAt,
	}
}

// filterPackageDepsView returns a filtered PackageDepsView. Layer filters keep
// only packages whose layer is allowed; cell focus filters keep only packages
// owned by cells that survived focus expansion. Stats are recomputed. Non-ready
// views are returned unchanged when there is no Graph to filter.
func filterPackageDepsView(v *PackageDepsView, filter Filter) *PackageDepsView {
	if len(filter.Layers) == 0 && len(filter.Cells) == 0 {
		return v
	}
	if v.Graph == nil {
		return v
	}
	layerSet := toStringSet(filter.Layers)
	cellSet := toStringSet(filter.Cells)
	nodes := make([]*kerneldepgraph.Node, 0, len(v.Graph.Packages))
	for _, n := range v.Graph.Packages {
		if len(layerSet) > 0 && !layerSet[n.Layer] {
			continue
		}
		if len(cellSet) > 0 && !cellSet[n.CellID] {
			continue
		}
		nodes = append(nodes, n)
	}
	return &PackageDepsView{
		Status: v.Status,
		Graph:  kerneldepgraph.FromNodes(v.Graph.Module, nodes),
		Error:  v.Error,
	}
}

// MarshalDocument serializes d as JSON or YAML according to format. Returns
// an error for unrecognized format strings.
//
//   - format == "json" → encoding/json with two-space indent
//   - format == "yaml" → gopkg.in/yaml.v3 default indent
//
// Both formats are byte-deterministic for identical input thanks to:
//   - Document field declaration order (struct fields encoded in source order)
//   - kerneldepgraph.Graph.MarshalJSON sorting Packages + Imports
//   - BuildDocument sorting Entities and Relations before returning
func MarshalDocument(d Document, format string) ([]byte, error) {
	switch format {
	case "json":
		return json.MarshalIndent(d, "", "  ")
	case "yaml":
		return yaml.Marshal(d)
	default:
		return nil, fmt.Errorf("metadata.MarshalDocument: unrecognized format %q (want json|yaml)", format)
	}
}
