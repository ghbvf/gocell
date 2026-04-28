// Package governance — select-targets impact analysis.
// Given a set of changed file paths, TargetSelector computes which slices,
// cells, contracts, and journeys are potentially affected.
// This is ADVISORY level — not a completeness guarantee.
package governance

import (
	"path"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
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
//  1. File path -> slice (via parsed cell/slice source directories)
//  2. Slice -> cell (via belongsToCell)
//  3. Slice -> contracts (via contractUsages)
//  4. Cell -> journeys (via journey.cells)
//  5. journeys/J-*.yaml -> journey -> cells -> slices + contracts
//  6. assemblies/{id}/assembly.yaml -> assembly -> cells -> slices
//
// ref: K8s kubectl diff — impact analysis across all resource types
func (ts *TargetSelector) SelectFromFiles(files []string) *AffectedTargets {
	sliceSet := make(map[string]struct{})
	fileCellSet := make(map[string]struct{}) // cells directly hit by file paths (cells/**)
	cellSet := make(map[string]struct{})     // cells from journey/assembly expansion
	contractSet := make(map[string]struct{})

	for _, f := range files {
		// Normalize path separators and clean.
		f = path.Clean(strings.ReplaceAll(f, "\\", "/"))

		if ts.matchSliceFromCellsPath(f, sliceSet, fileCellSet) {
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

	// Expand L0 dependencies BEFORE journey/assembly expansion.
	// Only file-path-derived cells trigger L0 propagation, not
	// journey/assembly references (which would cause over-selection).
	ts.expandL0Dependents(sliceSet, fileCellSet)

	// Expand cells collected from journey/assembly paths into slices.
	for key, s := range ts.project.Slices {
		if _, ok := cellSet[s.BelongsToCell]; ok {
			sliceSet[key] = struct{}{}
		}
	}

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

// matchSliceFromCellsPath handles paths under parsed cell directories
// (cells/* and examples/*/cells/*).
// Returns true if the path was consumed (matched a known cell path).
// fileCellSet tracks which cells are directly hit by file paths
// (used for L0 dependency propagation).
func (ts *TargetSelector) matchSliceFromCellsPath(f string, sliceSet, fileCellSet map[string]struct{}) bool {
	for key, s := range ts.project.Slices {
		if s.File == "" {
			continue
		}
		if pathWithin(f, path.Dir(s.File)) {
			sliceSet[key] = struct{}{}
			fileCellSet[s.BelongsToCell] = struct{}{}
			return true
		}
	}

	for cellID, c := range ts.project.Cells {
		if c.File == "" {
			continue
		}
		if !pathWithin(f, path.Dir(c.File)) {
			continue
		}
		fileCellSet[cellID] = struct{}{}
		for key, s := range ts.project.Slices {
			if s.BelongsToCell == cellID {
				sliceSet[key] = struct{}{}
			}
		}
		return true
	}
	return ts.matchLegacyCellsPath(f, sliceSet, fileCellSet)
}

func (ts *TargetSelector) matchLegacyCellsPath(f string, sliceSet, fileCellSet map[string]struct{}) bool {
	if !strings.HasPrefix(f, "cells/") {
		return false
	}
	parts := strings.Split(f, "/")
	if len(parts) < 2 {
		return true
	}
	cellID := parts[1]
	if _, ok := ts.project.Cells[cellID]; !ok {
		return true
	}
	fileCellSet[cellID] = struct{}{}
	if len(parts) >= 4 && parts[2] == "slices" {
		key := cellID + "/" + parts[3]
		if _, ok := ts.project.Slices[key]; ok {
			sliceSet[key] = struct{}{}
		}
		return true
	}
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
	matched := false
	for contractID, c := range ts.project.Contracts {
		if contractPathMatches(c, f) {
			ts.addSlicesForContract(contractID, sliceSet)
			matched = true
		}
	}
	if matched {
		return true
	}

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

	ts.addSlicesForContract(contractID, sliceSet)
	return true
}

func contractPathMatches(c *metadata.ContractMeta, f string) bool {
	if c == nil {
		return false
	}
	contractDir := metadata.ContractDirFromMeta(c)
	if contractDir != "" && pathWithin(f, contractDir) {
		return true
	}
	for _, ref := range metadata.ContractSchemaRefs(c) {
		if ref.Ref == "" || contractDir == "" {
			continue
		}
		refPath := strings.ReplaceAll(ref.Ref, "\\", "/")
		if path.Clean(path.Join(contractDir, refPath)) == f {
			return true
		}
	}
	return false
}

func (ts *TargetSelector) addSlicesForContract(contractID string, sliceSet map[string]struct{}) {
	for key, s := range ts.project.Slices {
		for _, cu := range s.ContractUsages {
			if cu.Contract == contractID {
				sliceSet[key] = struct{}{}
				break
			}
		}
	}
}

// matchFromJourneyPath handles paths under journeys/.
// Only J-*.yaml files are treated as journey files; other files (e.g.
// status-board.yaml) are ignored.
// Returns true if the path was consumed (matched journeys/ prefix).
func (ts *TargetSelector) matchFromJourneyPath(f string, cellSet map[string]struct{}, contractSet map[string]struct{}) bool {
	for _, journey := range ts.project.Journeys {
		if journey.File == "" || f != journey.File {
			continue
		}
		ts.addJourneyTargets(journey, cellSet, contractSet)
		return true
	}

	if !strings.HasPrefix(f, "journeys/") {
		return false
	}

	// Extract filename: journeys/J-ssologin.yaml -> J-ssologin.yaml
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

	ts.addJourneyTargets(journey, cellSet, contractSet)
	return true
}

func (ts *TargetSelector) addJourneyTargets(journey *metadata.JourneyMeta, cellSet map[string]struct{}, contractSet map[string]struct{}) {
	for _, cellID := range journey.Cells {
		cellSet[cellID] = struct{}{}
	}
	for _, contractID := range journey.Contracts {
		contractSet[contractID] = struct{}{}
	}
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

func pathWithin(file, dir string) bool {
	dir = strings.TrimSuffix(path.Clean(dir), "/")
	file = path.Clean(file)
	return file == dir || strings.HasPrefix(file, dir+"/")
}

// expandL0Dependents checks whether any file-change-affected cell is L0,
// and if so, adds all slices of cells that declare that L0 cell in their
// l0Dependencies. fileCellSet covers L0 cells that may have no slices
// (and thus no entries in sliceSet).
func (ts *TargetSelector) expandL0Dependents(sliceSet map[string]struct{}, fileCellSet map[string]struct{}) {
	l0Cells := ts.collectL0CellsFromSlices(sliceSet)
	ts.collectL0CellsFromFileSet(fileCellSet, l0Cells)
	if len(l0Cells) > 0 {
		ts.propagateL0DependentSlices(sliceSet, l0Cells)
	}
}

// collectL0CellsFromSlices builds a set of L0 cell IDs from the slice-key set.
// It looks up each slice's owning cell and checks its consistency level.
func (ts *TargetSelector) collectL0CellsFromSlices(sliceSet map[string]struct{}) map[string]struct{} {
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
		if lvl, err := cell.ParseLevel(c.ConsistencyLevel); err == nil && lvl == cell.L0 {
			l0Cells[c.ID] = struct{}{}
		}
	}
	return l0Cells
}

// collectL0CellsFromFileSet adds L0 cell IDs from the cell-level file-change set
// into the existing l0Cells map. This covers L0 cells that have no slices.
func (ts *TargetSelector) collectL0CellsFromFileSet(fileCellSet map[string]struct{}, l0Cells map[string]struct{}) {
	for cellID := range fileCellSet {
		c, ok := ts.project.Cells[cellID]
		if !ok {
			continue
		}
		if lvl, err := cell.ParseLevel(c.ConsistencyLevel); err == nil && lvl == cell.L0 {
			l0Cells[c.ID] = struct{}{}
		}
	}
}

// propagateL0DependentSlices adds all slices owned by cells that declare an
// l0Dependency on any cell in l0Cells into sliceSet.
func (ts *TargetSelector) propagateL0DependentSlices(sliceSet map[string]struct{}, l0Cells map[string]struct{}) {
	for _, c := range ts.project.Cells {
		if !cellDependsOnL0(c.L0Dependencies, l0Cells) {
			continue
		}
		for key, s := range ts.project.Slices {
			if s.BelongsToCell == c.ID {
				sliceSet[key] = struct{}{}
			}
		}
	}
}

// cellDependsOnL0 returns true if any of the given l0Dependencies targets a
// cell in the l0Cells set.
func cellDependsOnL0(deps []metadata.L0DepMeta, l0Cells map[string]struct{}) bool {
	for _, dep := range deps {
		if _, ok := l0Cells[dep.Cell]; ok {
			return true
		}
	}
	return false
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
