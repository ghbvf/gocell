package governance

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
)

// DependencyChecker validates structural dependencies between cells.
type DependencyChecker struct {
	project   *metadata.ProjectMeta
	cells     *registry.CellRegistry
	contracts *registry.ContractRegistry
}

// NewDependencyChecker creates a DependencyChecker for the given project metadata.
func NewDependencyChecker(project *metadata.ProjectMeta) *DependencyChecker {
	return &DependencyChecker{
		project:   project,
		cells:     registry.NewCellRegistry(project),
		contracts: registry.NewContractRegistry(project),
	}
}

// Check runs all dependency checks and returns findings.
func (dc *DependencyChecker) Check() []ValidationResult {
	if dc.project == nil {
		return nil
	}

	var results []ValidationResult
	results = append(results, dc.checkDEP01()...)
	results = append(results, dc.checkDEP02()...)
	results = append(results, dc.checkDEP03()...)
	return results
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
			results = append(results, ValidationResult{
				Code:      "DEP-01",
				Severity:  SeverityError,
				IssueType: IssueMismatch,
				File:      sliceFile(key),
				Field:     "belongsToCell",
				Message: fmt.Sprintf(
					"slice %q declares belongsToCell %q but is registered under cell %q",
					s.ID, s.BelongsToCell, keyCellID,
				),
			})
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
	var results []ValidationResult
	// Build adjacency list: consumerCell → set of providerCells.
	graph := make(map[string]map[string]bool)

	for _, s := range dc.project.Slices {
		providerCell := s.BelongsToCell
		for _, cu := range s.ContractUsages {
			if !isProviderRole(cu.Role) {
				continue
			}
			consumers, consErr := dc.contracts.Consumers(cu.Contract)
			if consErr != nil {
				results = append(results, ValidationResult{
					Code:      "DEP-02",
					Severity:  SeverityError,
					IssueType: IssueInvalid,
					File:      sliceFile(providerCell + "/" + s.ID),
					Field:     "contractUsages",
					Message: fmt.Sprintf(
						"cannot resolve consumers for contract %q: %v — dependency graph may be incomplete",
						cu.Contract, consErr,
					),
				})
				continue
			}
			for _, consumerCell := range consumers {
				if consumerCell == providerCell {
					continue // self-edge is not a cross-cell dependency
				}
				if graph[consumerCell] == nil {
					graph[consumerCell] = make(map[string]bool)
				}
				graph[consumerCell][providerCell] = true
			}
		}
	}

	// Also ensure all cells with no edges appear in the graph for completeness.
	for cellID := range dc.project.Cells {
		if graph[cellID] == nil {
			graph[cellID] = make(map[string]bool)
		}
	}

	// If any consumer resolution failed, the graph is incomplete and cycle
	// detection would produce unreliable results. Return errors immediately.
	if len(results) > 0 {
		return results
	}

	// Three-color DFS cycle detection.
	const (
		white = 0 // unvisited
		gray  = 1 // in current path
		black = 2 // fully explored
	)

	color := make(map[string]int)
	parent := make(map[string]string) // to reconstruct cycle path

	var cycle []string

	var dfs func(node string) bool
	dfs = func(node string) bool {
		color[node] = gray
		for neighbor := range graph[node] {
			switch color[neighbor] {
			case gray:
				// Found a cycle; reconstruct it.
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
		if color[node] == white {
			if dfs(node) {
				break
			}
		}
	}

	if len(cycle) > 0 {
		results = append(results, ValidationResult{
			Code:      "DEP-02",
			Severity:  SeverityError,
			IssueType: IssueForbidden,
			File:      "project",
			Field:     "cells",
			Message:   fmt.Sprintf("circular dependency detected: %s", strings.Join(cycle, " → ")),
		})
	}
	return results
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
			results = append(results, ValidationResult{
				Code:      "DEP-03",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      cellFile(c.ID),
				Field:     "l0Dependencies",
				Message: fmt.Sprintf(
					"cell %q has L0 dependencies but is not assigned to any assembly",
					c.ID,
				),
			})
			continue
		}
		for i, dep := range c.L0Dependencies {
			depAssembly := cellToAssembly[dep.Cell]
			if depAssembly == "" {
				results = append(results, ValidationResult{
					Code:      "DEP-03",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      cellFile(c.ID),
					Field:     fmt.Sprintf("l0Dependencies[%d].cell", i),
					Message: fmt.Sprintf(
						"cell %q (assembly %q) has L0 dependency on %q which is not in any assembly",
						c.ID, assemblyID, dep.Cell,
					),
				})
			} else if assemblyID != depAssembly {
				results = append(results, ValidationResult{
					Code:      "DEP-03",
					Severity:  SeverityError,
					IssueType: IssueMismatch,
					File:      cellFile(c.ID),
					Field:     fmt.Sprintf("l0Dependencies[%d].cell", i),
					Message: fmt.Sprintf(
						"cell %q (assembly %q) has L0 dependency on %q (assembly %q); both must be in the same assembly",
						c.ID, assemblyID, dep.Cell, depAssembly,
					),
				})
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
