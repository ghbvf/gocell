// locator_conventional.go — Conventional layout walker for Locator.
//
// This file is the funnel target for LOCATOR-DISCOVERY-FUNNEL-01 archtest:
//   - A1 (caller allowlist on fs.WalkDir): the only fs.WalkDir call in
//     kernel/metadata/** outside locator_manifest.go must reside here, inside
//     Locator.discoverConventional.
//   - A2 (form-uniqueness on path-prefix comparison): hardcoded path tokens
//     {cells, contracts, journeys, assemblies, examples} appear only inside
//     this file (and locator_manifest.go for default include patterns).
//
// The 5 path-pattern matchers were extracted verbatim from the original
// kernel/metadata/parser.go (matchCellYAML / matchSliceYAML / matchContractYAML
// / matchJourneyYAML / matchAssemblyYAML and their *FromPath siblings) when
// the locator funnel was introduced (M1 of the Operator-SDK + Workspace dual
// mode work — see ADR 202605281200).

package metadata

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// discoverConventional walks the on-disk root applying the hardcoded 5-pattern
// match. examples/ subtree is included via the same patterns with an extra
// "examples/<id>/" prefix per pattern.
func (l *Locator) discoverConventional() ([]MetadataSource, error) {
	var out []MetadataSource
	walkErr := fs.WalkDir(l.fsys, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Explicitly skip symlinks. The disk-backed fs is os.OpenRoot(root).FS()
		// (NewLocator), which already rejects symlink escapes at the syscall
		// layer; this skip is defense-in-depth for in-root symlink entries and
		// the fs.FS-backed path (NewLocatorFS / MapFS), avoiding traversal
		// through unexpected topology (e.g. symlink loops). Mirrors the same
		// guard in manifestGlobWalkFn.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		clean := filepath.ToSlash(p)
		if src, ok := classifyConventionalPath(clean); ok {
			out = append(out, src)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// classifyConventionalPath inspects a forward-slash path against the 5
// conventional patterns plus the 2 singletons. Returns the matching
// MetadataSource and true when the path is a recognized metadata file;
// returns the zero value and false otherwise.
//
// LOCATOR-DISCOVERY-FUNNEL-01 A2 allowlist: the path-token literals
// {cells, contracts, journeys, assemblies, examples} appear only inside
// the match*Path helpers below and inside this function's singleton checks.
func classifyConventionalPath(path string) (MetadataSource, bool) {
	if cellID, ok := matchCellPath(path); ok {
		return MetadataSource{Path: path, Kind: SourceCell, CellID: cellID}, true
	}
	if cellID, ok := matchSlicePath(path); ok {
		return MetadataSource{Path: path, Kind: SourceSlice, CellID: cellID}, true
	}
	if _, ok := matchContractPath(path); ok {
		return MetadataSource{Path: path, Kind: SourceContract}, true
	}
	if _, ok := matchJourneyPath(path); ok {
		return MetadataSource{Path: path, Kind: SourceJourney}, true
	}
	if matchAssemblyPath(path) {
		return MetadataSource{Path: path, Kind: SourceAssembly}, true
	}
	if path == "actors.yaml" {
		return MetadataSource{Path: path, Kind: SourceActors}, true
	}
	if path == "journeys/status-board.yaml" {
		return MetadataSource{Path: path, Kind: SourceStatusBoard}, true
	}
	return MetadataSource{}, false
}

// matchCellPath matches cells/*/cell.yaml, corecells/*/cell.yaml, and
// examples/*/cells/*/cell.yaml.
// Returns the cell directory name (== conventional cell ID) when matched.
func matchCellPath(path string) (string, bool) {
	parts := splitConventionalPath(path)
	if len(parts) == 3 && isTopLevelCellRoot(parts[0]) && parts[2] == "cell.yaml" {
		return parts[1], true
	}
	if len(parts) == 5 && parts[0] == "examples" && parts[2] == "cells" && parts[4] == "cell.yaml" {
		return parts[3], true
	}
	return "", false
}

// matchSlicePath matches cells/*/slices/*/slice.yaml,
// corecells/*/slices/*/slice.yaml, and
// examples/*/cells/*/slices/*/slice.yaml. Returns the cellID (== parent
// cell directory name) when matched. sliceID is intentionally not returned
// — parser derives the slice directory directly via path.Base(path.Dir())
// since slice.ID is the YAML-declared id, not a path-derived identity.
func matchSlicePath(path string) (cellDir string, ok bool) {
	parts := splitConventionalPath(path)
	if len(parts) == 5 && isTopLevelCellRoot(parts[0]) && parts[2] == "slices" && parts[4] == "slice.yaml" {
		return parts[1], true
	}
	if len(parts) == 7 && parts[0] == "examples" && parts[2] == "cells" && parts[4] == "slices" && parts[6] == "slice.yaml" {
		return parts[3], true
	}
	return "", false
}

func isTopLevelCellRoot(segment string) bool {
	return segment == "cells" || segment == "corecells"
}

// matchContractPath matches contracts/{kind}/{...}/contract.yaml and
// examples/*/contracts/{kind}/{...}/contract.yaml. Returns the contract
// directory (forward-slash, no trailing slash, no contract.yaml suffix).
func matchContractPath(path string) (string, bool) {
	parts := splitConventionalPath(path)
	if len(parts) >= 5 && parts[0] == "contracts" && parts[len(parts)-1] == "contract.yaml" {
		return strings.Join(parts[:len(parts)-1], "/"), true
	}
	if len(parts) >= 7 && parts[0] == "examples" && parts[2] == "contracts" && parts[len(parts)-1] == "contract.yaml" {
		return strings.Join(parts[:len(parts)-1], "/"), true
	}
	return "", false
}

// matchJourneyPath matches journeys/J-*.yaml and examples/*/journeys/J-*.yaml.
// Returns the journey ID (filename minus the .yaml extension).
func matchJourneyPath(path string) (string, bool) {
	parts := splitConventionalPath(path)
	var name string
	switch {
	case len(parts) == 2 && parts[0] == "journeys":
		name = parts[1]
	case len(parts) == 4 && parts[0] == "examples" && parts[2] == "journeys":
		name = parts[3]
	default:
		return "", false
	}
	if !strings.HasPrefix(name, "J-") || !strings.HasSuffix(name, ".yaml") {
		return "", false
	}
	return strings.TrimSuffix(name, ".yaml"), true
}

// matchAssemblyPath matches assemblies/*/assembly.yaml and
// examples/*/assembly.yaml.
func matchAssemblyPath(path string) bool {
	parts := splitConventionalPath(path)
	if len(parts) == 3 && parts[0] == "assemblies" && parts[2] == "assembly.yaml" {
		return true
	}
	return len(parts) == 3 && parts[0] == "examples" && parts[2] == "assembly.yaml"
}

// splitConventionalPath splits a forward-slash-separated path into its
// segments after normalising slashes. Used by the conventional matchers
// only.
func splitConventionalPath(path string) []string {
	clean := filepath.ToSlash(path)
	return strings.Split(clean, "/")
}
