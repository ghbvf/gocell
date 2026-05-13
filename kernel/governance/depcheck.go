package governance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
)

// Graph is the cell-level directed dependency graph produced by DependencyChecker.
// Nodes and Edges are deterministically sorted (Nodes alphabetically; Edges by
// From then To) so callers can byte-compare two Graph values.
type Graph struct {
	Nodes []string
	Edges []Edge
}

// Edge is a directed dependency between two cells: From depends on To.
type Edge struct {
	From string
	To   string
}

// Graph builds the cell dependency graph from the project metadata and returns
// it together with any resolution errors encountered during construction.
// The returned Graph is always fully sorted (Nodes and Edges) for determinism.
// If resolution errors are present the graph may be incomplete, but it still
// contains all nodes for cells that were resolved cleanly.
func (dc *DependencyChecker) Graph() (Graph, []ValidationResult) {
	if dc.project == nil {
		return Graph{Nodes: []string{}}, nil
	}
	raw, errs := dc.buildDependencyGraph()
	return rawGraphToGraph(raw), errs
}

// rawGraphToGraph converts the internal adjacency map to a sorted Graph.
func rawGraphToGraph(raw map[string]map[string]bool) Graph {
	nodes := make([]string, 0, len(raw))
	for n := range raw {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	var edges []Edge
	for _, from := range nodes {
		tos := make([]string, 0, len(raw[from]))
		for to := range raw[from] {
			tos = append(tos, to)
		}
		sort.Strings(tos)
		for _, to := range tos {
			edges = append(edges, Edge{From: from, To: to})
		}
	}

	return Graph{
		Nodes: nodes,
		Edges: edges,
	}
}

// DependencyChecker validates structural dependencies between cells. It
// embeds locator so locate/newResult and the project field are shared with
// Validator via a single implementation.
type DependencyChecker struct {
	locator
	cells     *registry.CellRegistry
	contracts *registry.ContractRegistry
}

// NewDependencyChecker creates a DependencyChecker for the given project metadata.
func NewDependencyChecker(project *metadata.ProjectMeta) *DependencyChecker {
	return &DependencyChecker{
		locator:   locator{project: project},
		cells:     registry.NewCellRegistry(project),
		contracts: registry.NewContractRegistry(project),
	}
}

// Check runs all dependency checks and returns findings.
func (dc *DependencyChecker) Check() []ValidationResult {
	var results []ValidationResult
	for _, check := range dc.checks() {
		results = append(results, check()...)
	}
	return results
}

// CheckFailFast runs the same checks as Check but returns as soon as any
// produces a SeverityError. Warnings do not trigger the bailout.
func (dc *DependencyChecker) CheckFailFast() []ValidationResult {
	var results []ValidationResult
	for _, check := range dc.checks() {
		r := check()
		results = append(results, r...)
		if HasErrors(r) {
			return results
		}
	}
	return results
}

// checks returns the list of check methods in execution order. Shared by
// Check and CheckFailFast so they stay provably in sync.
func (dc *DependencyChecker) checks() []func() []ValidationResult {
	if dc.project == nil {
		return nil
	}
	return []func() []ValidationResult{
		dc.checkDEP01, dc.checkDEP02, dc.checkDEP03,
	}
}

// checkDEP01 verifies that each slice's belongsToCell matches the cellID
// encoded in its map key ("cellID/sliceID").
func (dc *DependencyChecker) checkDEP01() []ValidationResult {
	var results []ValidationResult
	for key, s := range dc.project.Slices {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		keyCellID := parts[0]
		if s.BelongsToCell != keyCellID {
			results = append(results, dc.newResult(
				codeDEP01, SeverityError, IssueMismatch,
				sliceFile(s),
				"belongsToCell",
				fmt.Sprintf(
					"slice %q declares belongsToCell %q but is registered under cell %q;"+
						" fix: update belongsToCell to match the directory cell id or move the slice to the correct cell directory",
					s.ID, s.BelongsToCell, keyCellID,
				),
			))
		}
	}
	return results
}

// checkDEP02 verifies that the cell dependency graph (derived from contracts)
// contains no cycles.
//
// Graph construction: for each slice with a provider-role contractUsage, find
// the contract's consumers. Each consumer-cell depends on the provider-cell,
// yielding a directed edge consumer → provider.
// Cycle detection uses iterative DFS with three-color marking.
func (dc *DependencyChecker) checkDEP02() []ValidationResult {
	graph, buildErrs := dc.buildDependencyGraph()
	if len(buildErrs) > 0 {
		// Graph is incomplete — cycle detection would produce unreliable results.
		return buildErrs
	}
	cycle := detectCycle(graph)
	if len(cycle) > 0 {
		// DEP-02 spans the whole cell graph — no single file owns the cycle,
		// so we emit a scoped result ("project") rather than a fake file
		// path, which would mislead users into trying to click-jump to it.
		return []ValidationResult{dc.newScopedResult(
			codeDEP02, SeverityError, IssueForbidden,
			"project",
			"cells",
			fmt.Sprintf("circular dependency detected: %s;"+
				" fix: remove the dependency cycle by restructuring cell contracts", strings.Join(cycle, " → ")),
		)}
	}
	return nil
}

// buildDependencyGraph constructs the adjacency list consumerCell → set of
// providerCells from the slice contractUsages. All cells (even isolated ones)
// are added so cycle detection covers the full graph.
// Returns (graph, resolutionErrors); if resolutionErrors is non-empty the
// graph is incomplete and must not be used for cycle detection.
func (dc *DependencyChecker) buildDependencyGraph() (map[string]map[string]bool, []ValidationResult) {
	graph := make(map[string]map[string]bool)
	var errs []ValidationResult

	for _, s := range dc.project.Slices {
		errs = append(errs, dc.addSliceEdges(graph, s)...)
	}
	// Ensure isolated cells appear in the graph.
	for cellID := range dc.project.Cells {
		if graph[cellID] == nil {
			graph[cellID] = make(map[string]bool)
		}
	}
	return graph, errs
}

// addSliceEdges adds consumer → provider directed edges to graph for every
// provider-role contractUsage in s. Returns resolution errors if any contract's
// consumers cannot be resolved.
func (dc *DependencyChecker) addSliceEdges(graph map[string]map[string]bool, s *metadata.SliceMeta) []ValidationResult {
	providerCell := s.BelongsToCell
	var errs []ValidationResult
	for _, cu := range s.ContractUsages {
		if !isProviderRole(cu.Role) {
			continue
		}
		consumers, consErr := dc.contracts.Consumers(cu.Contract)
		if consErr != nil {
			errs = append(errs, dc.newResult(
				codeDEP02, SeverityError, IssueInvalid,
				sliceFile(s),
				"contractUsages",
				fmt.Sprintf(
					"cannot resolve consumers for contract %q: %v — dependency graph may be incomplete;"+
						" fix: ensure the contract exists and has valid consumer declarations",
					cu.Contract, consErr,
				),
			))
			continue
		}
		for _, consumerCell := range consumers {
			dc.addCellEdge(graph, consumerCell, providerCell)
		}
	}
	return errs
}

// addCellEdge adds a directed edge consumerCell → providerCell to graph,
// skipping self-edges and non-cell IDs (actor entries from actors.yaml).
func (dc *DependencyChecker) addCellEdge(graph map[string]map[string]bool, consumerCell, providerCell string) {
	if consumerCell == providerCell {
		return // self-edge is not a cross-cell dependency
	}
	// Skip actor IDs: actors.yaml entries participate in contracts but
	// are not cells — including them would pollute the cell dep graph.
	if _, isCell := dc.project.Cells[consumerCell]; !isCell {
		return
	}
	if graph[consumerCell] == nil {
		graph[consumerCell] = make(map[string]bool)
	}
	graph[consumerCell][providerCell] = true
}

// detectCycle runs three-color DFS on the directed graph and returns the
// first cycle found as a human-readable path (e.g. ["A", "B", "C", "A"]),
// or nil if the graph is acyclic.
func detectCycle(graph map[string]map[string]bool) []string {
	const (
		white = 0 // unvisited
		gray  = 1 // in current DFS path
		black = 2 // fully explored
	)
	color := make(map[string]int, len(graph))
	parent := make(map[string]string, len(graph))
	var cycle []string

	var dfs func(node string) bool
	dfs = func(node string) bool {
		color[node] = gray
		for neighbor := range graph[node] {
			switch color[neighbor] {
			case gray:
				cycle = reconstructCycle(parent, node, neighbor)
				return true
			case white:
				parent[neighbor] = node
				if dfs(neighbor) {
					return true
				}
			}
		}
		color[node] = black
		return false
	}

	for node := range graph {
		if color[node] == white && dfs(node) {
			break
		}
	}
	return cycle
}

// reconstructCycle traces parent pointers to build the cycle path from
// back-edge target (neighbor) through to current node.
func reconstructCycle(parent map[string]string, current, backTo string) []string {
	// Build path: backTo → ... → current → backTo
	path := []string{current}
	for n := current; n != backTo; {
		n = parent[n]
		path = append(path, n)
	}
	// Reverse to get backTo → ... → current
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	// Append backTo again to close the cycle.
	path = append(path, backTo)
	return path
}

// checkDEP03 verifies that all L0 dependencies of a cell are co-located in
// the same assembly.
func (dc *DependencyChecker) checkDEP03() []ValidationResult {
	if len(dc.project.Assemblies) == 0 {
		return nil
	}

	// Build reverse index: cellID → assemblyID.
	cellToAssembly := make(map[string]string)
	for _, a := range dc.project.Assemblies {
		for _, cellRef := range a.Cells {
			cellToAssembly[cellRef] = a.ID
		}
	}

	var results []ValidationResult
	for _, c := range dc.project.Cells {
		if len(c.L0Dependencies) == 0 {
			continue
		}
		assemblyID := cellToAssembly[c.ID]
		if assemblyID == "" {
			// Cell with L0 dependencies must be assigned to an assembly.
			results = append(results, dc.newResult(
				codeDEP03, SeverityError, IssueRequired,
				cellFile(c),
				"l0Dependencies",
				fmt.Sprintf(
					"cell %q has L0 dependencies but is not assigned to any assembly; fix: add this cell to an assembly in assemblies/",
					c.ID,
				),
			))
			continue
		}
		for i, dep := range c.L0Dependencies {
			depAssembly := cellToAssembly[dep.Cell]
			if depAssembly == "" {
				results = append(results, dc.newResult(
					codeDEP03, SeverityError, IssueRequired,
					cellFile(c),
					fmt.Sprintf("l0Dependencies[%d].cell", i),
					fmt.Sprintf(
						"cell %q (assembly %q) has L0 dependency on %q which is not in any assembly; fix: add the dependency cell to an assembly",
						c.ID, assemblyID, dep.Cell,
					),
				))
			} else if assemblyID != depAssembly {
				results = append(results, dc.newResult(
					codeDEP03, SeverityError, IssueMismatch,
					cellFile(c),
					fmt.Sprintf("l0Dependencies[%d].cell", i),
					fmt.Sprintf(
						"cell %q (assembly %q) has L0 dependency on %q (assembly %q); both must be in the same assembly;"+
							" fix: move both cells to the same assembly",
						c.ID, assemblyID, dep.Cell, depAssembly,
					),
				))
			}
		}
	}
	return results
}

// isProviderRole returns true if the role string is a provider-side role.
func isProviderRole(role string) bool {
	switch role {
	case "serve", "publish", "handle", "provide":
		return true
	default:
		return false
	}
}
