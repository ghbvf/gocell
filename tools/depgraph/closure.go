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
// reachable internal imports. Each call returns a fresh map; the caller
// owns it and may mutate freely. The cost is one DFS per call (O(E) in
// production-edge count for the reachable subgraph), which is dominated
// by the underlying packages.Load on archtest's call sites.
func (g *Graph) TransitiveImports(id string) map[string]bool {
	if g == nil {
		return map[string]bool{}
	}
	visited := make(map[string]bool)
	g.dfs(id, visited)
	delete(visited, id) // exclude self
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
