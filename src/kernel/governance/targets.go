// Package governance — select-targets impact analysis.
// Given a set of changed file paths, TargetSelector computes which slices,
// cells, contracts, and journeys are potentially affected.
// This is ADVISORY level — not a completeness guarantee.
package governance

import (
	"path"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// AffectedTargets holds the impact analysis result.
type AffectedTargets struct {
	Slices    []string // affected slice IDs ("cellID/sliceID" format)
	Cells     []string // affected cell IDs
	Journeys  []string // affected journey IDs
	Contracts []string // affected contract IDs
}

// TargetSelector computes affected targets from file changes.
// This is ADVISORY level — not a completeness guarantee.
type TargetSelector struct {
	project *metadata.ProjectMeta
}

// NewTargetSelector creates a TargetSelector for the given project metadata.
func NewTargetSelector(project *metadata.ProjectMeta) *TargetSelector {
	return &TargetSelector{project: project}
}

// SelectFromFiles takes changed file paths (relative to project root)
// and returns affected targets.
//
// Mapping logic:
//  1. File path -> slice (via directory convention: cells/{cellID}/slices/{sliceID}/**)
//  2. Slice -> cell (via belongsToCell)
//  3. Slice -> contracts (via contractUsages)
//  4. Cell -> journeys (via journey.cells)
//  5. journeys/J-*.yaml -> journey -> cells -> slices + contracts
//  6. assemblies/{id}/assembly.yaml -> assembly -> cells -> slices
//
// ref: K8s kubectl diff — impact analysis across all resource types
func (ts *TargetSelector) SelectFromFiles(files []string) *AffectedTargets {
	sliceSet := make(map[string]struct{})
	cellSet := make(map[string]struct{})
	contractSet := make(map[string]struct{})

	for _, f := range files {
		// Normalize path separators and clean.
		f = path.Clean(strings.ReplaceAll(f, "\\", "/"))

		if ts.matchSliceFromCellsPath(f, sliceSet) {
			continue
		}
		if ts.matchSlicesFromContractPath(f, sliceSet) {
			continue
		}
		if ts.matchFromJourneyPath(f, cellSet, contractSet) {
			continue
		}
		ts.matchFromAssemblyPath(f, cellSet)
	}

	// Expand cells collected from journey/assembly paths into slices.
	for key, s := range ts.project.Slices {
		if _, ok := cellSet[s.BelongsToCell]; ok {
			sliceSet[key] = struct{}{}
		}
	}

	// Expand L0 dependencies: when an L0 cell's file is changed,
	// all cells that declare it in l0Dependencies are also affected.
	ts.expandL0Dependents(sliceSet)

	result := ts.expandFromSlices(sliceSet)

	// Merge extra contracts from journey paths that may not be referenced
	// by any slice's contractUsages.
	if len(contractSet) > 0 {
		merged := make(map[string]struct{})
		for _, c := range result.Contracts {
			merged[c] = struct{}{}
		}
		for c := range contractSet {
			merged[c] = struct{}{}
		}
		result.Contracts = sortedKeys(merged)
	}

	return result
}

// SelectFromSlice takes a slice key ("cellID/sliceID") and expands
// to all affected targets.
func (ts *TargetSelector) SelectFromSlice(sliceKey string) *AffectedTargets {
	sliceSet := make(map[string]struct{})
	if _, ok := ts.project.Slices[sliceKey]; ok {
		sliceSet[sliceKey] = struct{}{}
	}
	return ts.expandFromSlices(sliceSet)
}

// matchSliceFromCellsPath handles paths under cells/.
// Returns true if the path was consumed (matched cells/ prefix).
func (ts *TargetSelector) matchSliceFromCellsPath(f string, sliceSet map[string]struct{}) bool {
	// Expect: cells/{cellID}/...
	if !strings.HasPrefix(f, "cells/") {
		return false
	}

	parts := strings.Split(f, "/")
	// parts[0] = "cells", parts[1] = cellID, ...
	if len(parts) < 2 {
		return true // malformed, but under cells/
	}
	cellID := parts[1]

	// Check if the cell exists in the project.
	if _, ok := ts.project.Cells[cellID]; !ok {
		return true
	}

	// cells/{cellID}/slices/{sliceID}/**
	if len(parts) >= 4 && parts[2] == "slices" {
		sliceID := parts[3]
		key := cellID + "/" + sliceID
		if _, ok := ts.project.Slices[key]; ok {
			sliceSet[key] = struct{}{}
		}
		return true
	}

	// cells/{cellID}/** (non-slices path) -> all slices of that cell.
	for key, s := range ts.project.Slices {
		if s.BelongsToCell == cellID {
			sliceSet[key] = struct{}{}
		}
	}
	return true
}

// matchSlicesFromContractPath handles paths under contracts/.
// It derives the contract ID from the directory path and finds all slices
// that reference that contract via contractUsages.
// Returns true if the path was consumed (matched contracts/ prefix).
func (ts *TargetSelector) matchSlicesFromContractPath(f string, sliceSet map[string]struct{}) bool {
	if !strings.HasPrefix(f, "contracts/") {
		return false
	}

	// Contract directory: contracts/{kind}/{domain...}/{version}/
	// Contract ID: {kind}.{domain...}.{version}
	// Example: contracts/http/auth/login/v1/contract.yaml -> http.auth.login.v1
	contractID := ts.contractIDFromPath(f)
	if contractID == "" {
		return true
	}

	// Check that contract exists.
	if _, ok := ts.project.Contracts[contractID]; !ok {
		return true
	}

	// Reverse lookup: find all slices that use this contract.
	for key, s := range ts.project.Slices {
		for _, cu := range s.ContractUsages {
			if cu.Contract == contractID {
				sliceSet[key] = struct{}{}
				break
			}
		}
	}
	return true
}

// matchFromJourneyPath handles paths under journeys/.
// Only J-*.yaml files are treated as journey files; other files (e.g.
// status-board.yaml) are ignored.
// Returns true if the path was consumed (matched journeys/ prefix).
func (ts *TargetSelector) matchFromJourneyPath(f string, cellSet map[string]struct{}, contractSet map[string]struct{}) bool {
	if !strings.HasPrefix(f, "journeys/") {
		return false
	}

	// Extract filename: journeys/J-sso-login.yaml -> J-sso-login.yaml
	base := path.Base(f)

	// Only J-*.yaml files are journey definitions.
	if !strings.HasPrefix(base, "J-") || !strings.HasSuffix(base, ".yaml") {
		return true
	}

	// Journey ID is the filename without the .yaml extension.
	journeyID := strings.TrimSuffix(base, ".yaml")

	journey, ok := ts.project.Journeys[journeyID]
	if !ok {
		return true
	}

	// Add journey's cells to the cell set.
	for _, cellID := range journey.Cells {
		cellSet[cellID] = struct{}{}
	}

	// Add journey's contracts to the contract set.
	for _, contractID := range journey.Contracts {
		contractSet[contractID] = struct{}{}
	}

	return true
}

// matchFromAssemblyPath handles paths under assemblies/.
// Expects: assemblies/{id}/assembly.yaml
// Returns true if the path was consumed (matched assemblies/ prefix).
func (ts *TargetSelector) matchFromAssemblyPath(f string, cellSet map[string]struct{}) bool {
	if !strings.HasPrefix(f, "assemblies/") {
		return false
	}

	parts := strings.Split(f, "/")
	// parts[0] = "assemblies", parts[1] = assemblyID, ...
	if len(parts) < 2 {
		return true
	}
	assemblyID := parts[1]

	asm, ok := ts.project.Assemblies[assemblyID]
	if !ok {
		return true
	}

	// Add assembly's cells to the cell set.
	for _, cellID := range asm.Cells {
		cellSet[cellID] = struct{}{}
	}

	return true
}

// contractIDFromPath extracts a contract ID from a file path under contracts/.
// It takes everything between "contracts/" and the filename, strips the trailing
// slash, and joins with dots.
// Example: "contracts/http/auth/login/v1/contract.yaml" -> "http.auth.login.v1"
func (ts *TargetSelector) contractIDFromPath(f string) string {
	// Remove the "contracts/" prefix.
	rest := strings.TrimPrefix(f, "contracts/")
	if rest == "" || rest == f {
		return ""
	}

	// Get the directory part (remove the filename).
	dir := path.Dir(rest)
	if dir == "." || dir == "" {
		return ""
	}

	// Replace slashes with dots to form the contract ID.
	return strings.ReplaceAll(dir, "/", ".")
}

// expandL0Dependents checks whether any already-selected slice belongs to
// an L0 cell, and if so, adds all slices of cells that declare that L0 cell
// in their l0Dependencies. This propagates change impact through L0 edges.
func (ts *TargetSelector) expandL0Dependents(sliceSet map[string]struct{}) {
	// Collect L0 cell IDs that are already affected.
	l0Cells := make(map[string]struct{})
	for key := range sliceSet {
		s, ok := ts.project.Slices[key]
		if !ok {
			continue
		}
		c, ok := ts.project.Cells[s.BelongsToCell]
		if !ok {
			continue
		}
		if c.ConsistencyLevel == "L0" {
			l0Cells[c.ID] = struct{}{}
		}
	}
	if len(l0Cells) == 0 {
		return
	}

	// Find all cells that depend on any affected L0 cell.
	for _, c := range ts.project.Cells {
		for _, dep := range c.L0Dependencies {
			if _, ok := l0Cells[dep.Cell]; ok {
				// Add all slices of the dependent cell.
				for key, s := range ts.project.Slices {
					if s.BelongsToCell == c.ID {
						sliceSet[key] = struct{}{}
					}
				}
				break // no need to check more deps for this cell
			}
		}
	}
}

// expandFromSlices takes a set of slice keys and expands to the full
// AffectedTargets including cells, contracts, and journeys.
func (ts *TargetSelector) expandFromSlices(sliceSet map[string]struct{}) *AffectedTargets {
	cellSet := make(map[string]struct{})
	contractSet := make(map[string]struct{})
	journeySet := make(map[string]struct{})

	// Expand slices -> cells + contracts.
	for key := range sliceSet {
		s, ok := ts.project.Slices[key]
		if !ok {
			continue
		}
		cellSet[s.BelongsToCell] = struct{}{}
		for _, cu := range s.ContractUsages {
			contractSet[cu.Contract] = struct{}{}
		}
	}

	// Expand cells -> journeys.
	for jID, j := range ts.project.Journeys {
		for _, jCell := range j.Cells {
			if _, ok := cellSet[jCell]; ok {
				journeySet[jID] = struct{}{}
				break
			}
		}
	}

	return &AffectedTargets{
		Slices:    sortedKeys(sliceSet),
		Cells:     sortedKeys(cellSet),
		Journeys:  sortedKeys(journeySet),
		Contracts: sortedKeys(contractSet),
	}
}

// sortedKeys returns the keys of a set as a sorted slice.
// Returns nil (not empty slice) when the set is empty.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
