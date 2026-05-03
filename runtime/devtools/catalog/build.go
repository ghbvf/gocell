// Package catalog — build.go: BuildDocument and all entity/filter helpers.
package catalog

import (
	"fmt"
	"sort"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// ExportOptions configures BuildDocument. Clock is required; all other fields
// are optional. nil Clock returns an error.
type ExportOptions struct {
	// Clock is the time source used to stamp Document.GeneratedAt. Required.
	Clock clock.Clock
	// Root is the project root path echoed back in Document.Root. Optional.
	Root string
	// Filter projects entities and dependencies. Zero value = full snapshot.
	Filter Filter
	// CellDeps, when non-nil, populates Document.Dependencies.Cells. Caller
	// constructs by invoking governance.DependencyChecker.Graph() and
	// translating to *CellDepGraph.
	CellDeps *CellDepGraph
	// Packages, when non-nil, populates Document.Dependencies.Packages. The
	// pointer carries its own error field so HTTP lazy loading and CLI
	// synchronous loading share one shape. Callers signal readiness via
	// Graph != nil and errors via Error != "".
	Packages *PackageDepsView
}

// BuildDocument projects pm into a Document according to opts. Pure function:
// no I/O, no global state access. Time is sourced from opts.Clock.
//
// Returns an error only when opts is structurally invalid (e.g. opts.Clock is
// nil). All filter combinations produce a valid (possibly empty) Document; an
// empty result is not an error.
func BuildDocument(pm *metadata.ProjectMeta, opts ExportOptions) (Document, error) {
	if opts.Clock == nil {
		return Document{}, fmt.Errorf("catalog.BuildDocument: opts.Clock must not be nil")
	}
	if pm == nil {
		pm = &metadata.ProjectMeta{}
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
		GeneratedAt:   opts.Clock.Now().UTC().Format(time.RFC3339),
		Root:          opts.Root,
		Query:         buildFilterEcho(opts.Filter),
		Entities:      entities,
	}

	if filter.Include.StatusBoard {
		doc.StatusBoard = redactStatusBoard(pm.StatusBoard)
	}

	// Pass the expanded filter for dependency graph filtering.
	optsWithExpandedFilter := opts
	optsWithExpandedFilter.Filter = filter
	doc.Dependencies = buildDependencies(optsWithExpandedFilter, entities)

	return doc, nil
}

// buildEntities collects all entities from pm, applying relations based on include options.
func buildEntities(pm *metadata.ProjectMeta, filter Filter) []Entity {
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
func buildCellEntity(c *metadata.CellMeta, inc IncludeOptions) Entity {
	l0Deps := make([]CellSpecL0Dep, 0, len(c.L0Dependencies))
	for _, d := range c.L0Dependencies {
		l0Deps = append(l0Deps, CellSpecL0Dep{Cell: d.Cell, Reason: d.Reason})
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
	if inc.Relations {
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
func buildSliceEntity(s *metadata.SliceMeta, inc IncludeOptions) Entity {
	usages := make([]SliceSpecContractUsage, 0, len(s.ContractUsages))
	for _, u := range s.ContractUsages {
		usages = append(usages, SliceSpecContractUsage{Contract: u.Contract, Role: u.Role})
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
	if inc.Relations {
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
func buildContractEntity(c *metadata.ContractMeta, inc IncludeOptions) Entity {
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
	if inc.Relations && c.OwnerCell != "" {
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
func buildJourneyEntity(j *metadata.JourneyMeta, inc IncludeOptions) Entity {
	crit := make([]JourneyPassCrit, 0, len(j.PassCriteria))
	for _, p := range j.PassCriteria {
		crit = append(crit, JourneyPassCrit{Text: p.Text, Mode: p.Mode, CheckRef: p.CheckRef})
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
	if inc.Relations {
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
func buildAssemblyEntity(a *metadata.AssemblyMeta, inc IncludeOptions) Entity {
	spec := AssemblySpec{
		Cells: a.Cells,
		Build: AssemblySpecBuild{
			Entrypoint:     a.Build.Entrypoint,
			Binary:         a.Build.Binary,
			DeployTemplate: a.Build.DeployTemplate,
		},
	}

	var rels []Relation
	if inc.Relations {
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
func buildActorEntity(a metadata.ActorMeta, _ IncludeOptions) Entity {
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
func expandCellFocusSet(pm *metadata.ProjectMeta, focus []string) []string {
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
func addL0Deps(pm *metadata.ProjectMeta, expanded map[string]bool, focus []string) {
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
func buildContractMaps(pm *metadata.ProjectMeta) (ownerMap map[string]string, clientsMap map[string][]string) {
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
func contractConsumers(c *metadata.ContractMeta) []string {
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
func addContractClients(pm *metadata.ProjectMeta, expanded map[string]bool, ownerMap map[string]string, clientsMap map[string][]string) {
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
func addContractOwners(pm *metadata.ProjectMeta, expanded map[string]bool, ownerMap map[string]string) {
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

// applyFilter trims doc.Entities according to filter using strict AND semantics
// across kinds × layers × cells:
//   - When all three are non-empty, an entity must match all three to survive.
//   - In particular, ?layers=cells&cells=foo will drop Contract/Journey
//     entities owned by foo because their entityLayer is "contracts"/"journeys",
//     not "cells". To get a focus view that includes a cell's contracts and
//     journeys, use ?cells=foo without ?layers.
//
// This AND policy is intentional: callers expressing both axes mean "the
// intersection". Document changes here MUST be mirrored in
// docs/guides/devtools-catalog.md "多维过滤 AND 语义" section.
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
// by cell focus.
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

// buildIncludeEcho converts IncludeOptions to a sorted []string of active flag
// names. Returns nil (not an empty slice) when no flags are set so that
// omitempty on FilterEcho.Include elides the key from the wire output,
// consistent with Kinds/Layers/Cells (which are also omitted when empty).
func buildIncludeEcho(inc IncludeOptions) []string {
	var names []string
	if inc.CellDeps {
		names = append(names, "cellDeps")
	}
	if inc.PackageDeps {
		names = append(names, "packageDeps")
	}
	if inc.Relations {
		names = append(names, "relations")
	}
	if inc.StatusBoard {
		names = append(names, "statusBoard")
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return names
}

// buildDependencies builds the Dependencies block from opts, applying the same
// filter used for entities so focused views stay consistent.
func buildDependencies(opts ExportOptions, entities []Entity) *Dependencies {
	var deps Dependencies
	hasDeps := false

	if opts.Filter.Include.CellDeps && opts.CellDeps != nil {
		filtered := filterCellDepGraph(opts.CellDeps, opts.Filter, entities)
		deps.Cells = filtered
		hasDeps = true
	}
	if opts.Filter.Include.PackageDeps && opts.Packages != nil {
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
// owned by cells that survived focus expansion. Non-ready views (Graph == nil)
// are returned unchanged when there is no Graph to filter.
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
		Graph: kerneldepgraph.FromNodes(v.Graph.Module, nodes),
		Error: v.Error,
	}
}
