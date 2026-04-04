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
func (ts *TargetSelector) SelectFromFiles(files []string) *AffectedTargets {
	sliceSet := make(map[string]struct{})

	for _, f := range files {
		// Normalize path separators and clean.
		f = path.Clean(strings.ReplaceAll(f, "\\", "/"))

		if ts.matchSliceFromCellsPath(f, sliceSet) {
			continue
		}
		ts.matchSlicesFromContractPath(f, sliceSet)
	}

	return ts.expandFromSlices(sliceSet)
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
func (ts *TargetSelector) matchSlicesFromContractPath(f string, sliceSet map[string]struct{}) {
	if !strings.HasPrefix(f, "contracts/") {
		return
	}

	// Contract directory: contracts/{kind}/{domain...}/{version}/
	// Contract ID: {kind}.{domain...}.{version}
	// Example: contracts/http/auth/login/v1/contract.yaml -> http.auth.login.v1
	contractID := ts.contractIDFromPath(f)
	if contractID == "" {
		return
	}

	// Check that contract exists.
	if _, ok := ts.project.Contracts[contractID]; !ok {
		return
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
