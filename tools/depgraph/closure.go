package depgraph

import "strings"

// TransitiveImports returns the set of all packages reachable from id
// through production edges, restricted to packages inside the same
// module. The set excludes:
//
//   - id itself
//   - stdlib and other modules (closure stops at module boundary)
//   - test-only packages (Node.TestOnly == true)
//   - ghost nodes (paths referenced but not loaded into the graph)
//
// These exclusions are tuned for archtest layer rules: cross-cell
// laundering paths inside the module matter; stdlib / vendor
// reachability does not.
//
// Returns an empty (non-nil) map when id is not in the graph or has no
// reachable internal imports. Each call returns a fresh map; the caller
// owns it and may mutate freely. Implemented on top of
// TransitiveImportsWithPaths; use that variant directly when violation
// messages need the laundering chain (src → util → dep).
func (g *Graph) TransitiveImports(id string) map[string]bool {
	paths := g.TransitiveImportsWithPaths(id)
	out := make(map[string]bool, len(paths))
	for dep := range paths {
		out[dep] = true
	}
	return out
}

// TransitiveImportsWithPaths returns the same set as TransitiveImports
// keyed as dep → discovery-order path. Each value is the chain of
// package IDs from id to dep, inclusive on both ends (path[0] == id,
// path[len-1] == dep). Cycles are broken at first encounter and the
// recorded path is the DFS discovery path.
//
// Used by archtest's LAYER-05T/06T/09T violation messages to render
// "src → util → dep" so reviewers can locate the offending intermediary
// without grepping the codebase.
func (g *Graph) TransitiveImportsWithPaths(id string) map[string][]string {
	if g == nil {
		return map[string][]string{}
	}
	paths := make(map[string][]string)
	g.dfs(id, []string{id}, paths)
	delete(paths, id) // exclude self from the result set
	return paths
}

// dfs walks module-internal edges starting at id, recording the
// discovery path to each reachable node. Out-of-module, test-only, and
// ghost nodes are not traversed and do not appear in paths.
//
// Ghost nodes (id present in another package's Imports but not loaded
// into the graph) intentionally do not mark visited — there is no
// subtree to break a cycle on. Re-encountering the same ghost across
// different parents is harmless: the nil check short-circuits in O(1).
func (g *Graph) dfs(id string, currentPath []string, paths map[string][]string) {
	if _, seen := paths[id]; seen {
		return
	}
	if !g.inModule(id) {
		return
	}
	node := g.byID[id]
	if node == nil {
		return
	}
	if node.TestOnly {
		return
	}
	paths[id] = append([]string(nil), currentPath...)
	for _, dep := range node.Imports {
		g.dfs(dep, append(currentPath, dep), paths)
	}
}

// inModule reports whether id is the module root or any package below it.
func (g *Graph) inModule(id string) bool {
	if g == nil || g.Module == "" {
		return false
	}
	return id == g.Module || strings.HasPrefix(id, g.Module+"/")
}
