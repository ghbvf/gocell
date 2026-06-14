package governance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// Graph is the cell-level directed dependency graph produced by Graph().
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
func (v *Validator) Graph() (Graph, []ValidationResult) {
	if v.project == nil {
		return Graph{Nodes: []string{}}, nil
	}
	raw, errs := v.buildDependencyGraph()
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

// =============================================================================
// DEP rules (PhaseDep) — run by `gocell validate` via allRules. The cell and
// contract registries they consult live on *Validator (built in NewValidator).
// =============================================================================

// checkDEP01 verifies that each slice's belongsToCell matches the cellID
// encoded in its map key ("cellID/sliceID").
func (v *Validator) checkDEP01() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		keyCellID := parts[0]
		if s.BelongsToCell != keyCellID {
			results = append(results, v.newError(
				codeDEP01, IssueMismatch,
				sliceFile(s),
				"belongsToCell",
				fmt.Sprintf(
					"slice %q declares belongsToCell %q but is registered under cell %q",
					s.ID, s.BelongsToCell, keyCellID,
				),
				"update belongsToCell to match the directory cell id or move the slice to the correct cell directory",
			))
		}
	}
	return results
}

// checkDEP02 verifies that the cell dependency graph (derived from contracts)
// contains no cycles.
func (v *Validator) checkDEP02() []ValidationResult {
	graph, buildErrs := v.buildDependencyGraph()
	if len(buildErrs) > 0 {
		return buildErrs
	}
	cycle := detectCycle(graph)
	if len(cycle) > 0 {
		return []ValidationResult{v.newScopedError(
			codeDEP02, IssueForbidden,
			"project",
			"cells",
			fmt.Sprintf("circular dependency detected: %s", strings.Join(cycle, " → ")),
			"remove the dependency cycle by restructuring cell contracts",
		)}
	}
	return nil
}

// buildDependencyGraph constructs the adjacency list consumerCell → set of
// providerCells from the slice contractUsages.
func (v *Validator) buildDependencyGraph() (map[string]map[string]bool, []ValidationResult) {
	graph := make(map[string]map[string]bool)
	var errs []ValidationResult

	for _, s := range v.project.Slices {
		errs = append(errs, v.addSliceEdges(graph, s)...)
	}
	for cellID := range v.project.Cells {
		if graph[cellID] == nil {
			graph[cellID] = make(map[string]bool)
		}
	}
	return graph, errs
}

// addSliceEdges adds consumer → provider directed edges to graph for every
// provider-role contractUsage in s.
func (v *Validator) addSliceEdges(graph map[string]map[string]bool, s *metadata.SliceMeta) []ValidationResult {
	providerCell := s.BelongsToCell
	var errs []ValidationResult
	for _, cu := range s.ContractUsages {
		if !isProviderRole(cu.Role) {
			continue
		}
		consumers, consErr := v.contracts.Consumers(cu.Contract)
		if consErr != nil {
			errs = append(errs, v.newError(
				codeDEP02, IssueInvalid,
				sliceFile(s),
				"contractUsages",
				fmt.Sprintf(
					"cannot resolve consumers for contract %q: %v — dependency graph may be incomplete",
					cu.Contract, consErr,
				),
				"ensure the contract exists and has valid consumer declarations",
			))
			continue
		}
		for _, consumerCell := range consumers {
			v.addCellEdge(graph, consumerCell, providerCell)
		}
	}
	return errs
}

// addCellEdge adds a directed edge consumerCell → providerCell to graph,
// skipping self-edges and non-cell IDs.
func (v *Validator) addCellEdge(graph map[string]map[string]bool, consumerCell, providerCell string) {
	if consumerCell == providerCell {
		return
	}
	if _, isCell := v.project.Cells[consumerCell]; !isCell {
		return
	}
	if graph[consumerCell] == nil {
		graph[consumerCell] = make(map[string]bool)
	}
	graph[consumerCell][providerCell] = true
}

// checkDEP03 verifies that all L0 dependencies of a cell are co-located in
// the same assembly.
func (v *Validator) checkDEP03() []ValidationResult {
	if len(v.project.Assemblies) == 0 {
		return nil
	}

	cellToAssembly := make(map[string]string)
	for _, a := range v.project.Assemblies {
		for _, ref := range a.Cells {
			cellToAssembly[ref.ID] = a.ID
		}
	}

	var results []ValidationResult
	for _, c := range v.project.Cells {
		if len(c.L0Dependencies) == 0 {
			continue
		}
		assemblyID := cellToAssembly[c.ID]
		if assemblyID == "" {
			results = append(results, v.newError(
				codeDEP03, IssueRequired,
				cellFile(c),
				"l0Dependencies",
				fmt.Sprintf(
					"cell %q has L0 dependencies but is not assigned to any assembly",
					c.ID,
				),
				"add this cell to an assembly in assemblies/",
			))
			continue
		}
		for i, dep := range c.L0Dependencies {
			depAssembly := cellToAssembly[dep.Cell]
			if depAssembly == "" {
				results = append(results, v.newError(
					codeDEP03, IssueRequired,
					cellFile(c),
					fmt.Sprintf("l0Dependencies[%d].cell", i),
					fmt.Sprintf(
						"cell %q (assembly %q) has L0 dependency on %q which is not in any assembly",
						c.ID, assemblyID, dep.Cell,
					),
					"add the dependency cell to an assembly",
				))
			} else if assemblyID != depAssembly {
				results = append(results, v.newError(
					codeDEP03, IssueMismatch,
					cellFile(c),
					fmt.Sprintf("l0Dependencies[%d].cell", i),
					fmt.Sprintf(
						"cell %q (assembly %q) has L0 dependency on %q (assembly %q); both must be in the same assembly",
						c.ID, assemblyID, dep.Cell, depAssembly,
					),
					"move both cells to the same assembly",
				))
			}
		}
	}
	return results
}

// =============================================================================
// Free functions (shared by the *Validator DEP methods)
// =============================================================================

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
	path := []string{current}
	for n := current; n != backTo; {
		n = parent[n]
		path = append(path, n)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	path = append(path, backTo)
	return path
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
