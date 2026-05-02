package depgraph

import "strings"

// TransitiveImports returns the set of all packages reachable from id
// through production edges, restricted to packages inside the same
// module. The set excludes:
//
//   - id itself
//   - stdlib and other modules (closure stops at module boundary)
//   - test-only packages (Node.TestOnly == true)
//
// These exclusions are tuned for archtest layer rules: cross-cell
// laundering paths inside the module matter; stdlib / vendor
// reachability does not.
//
// Returns an empty (non-nil) map when id is not in the graph or has no
// reachable internal imports. Results are memoized per Graph.
func (g *Graph) TransitiveImports(id string) map[string]bool {
	if g == nil {
		return map[string]bool{}
	}
	if g.closure == nil {
		g.closure = make(map[string]map[string]bool, len(g.Packages))
	}
	if cached, ok := g.closure[id]; ok {
		return cached
	}
	visited := make(map[string]bool)
	g.dfs(id, visited)
	delete(visited, id) // exclude self
	g.closure[id] = visited
	return visited
}

// dfs walks module-internal edges starting at id, populating visited.
// Out-of-module and test-only nodes are not traversed.
func (g *Graph) dfs(id string, visited map[string]bool) {
	if visited[id] {
		return
	}
	if !g.inModule(id) {
		return
	}
	node := g.byID[id]
	if node == nil {
		visited[id] = true // unknown internal node, mark to break cycles
		return
	}
	if node.TestOnly {
		return
	}
	visited[id] = true
	for _, dep := range node.Imports {
		g.dfs(dep, visited)
	}
}

// inModule reports whether id is the module root or any package below it.
func (g *Graph) inModule(id string) bool {
	if g == nil || g.Module == "" {
		return false
	}
	return id == g.Module || strings.HasPrefix(id, g.Module+"/")
}
