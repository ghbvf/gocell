package metadata

import (
	"path"
	"path/filepath"
	"strings"
)

// CellRootKind identifies the supported cell layout families.
type CellRootKind string

const (
	CellRootLocal    CellRootKind = "local"
	CellRootPlatform CellRootKind = "platform"
	CellRootExample  CellRootKind = "example"
)

// CellLocation is the layout-derived identity for a cell-owned path.
type CellLocation struct {
	ID       string
	Kind     CellRootKind
	RootRel  string
	ModuleID string
}

// CellLocationFromRel returns the cell identity for a module-relative path
// under cells/*, corecells/*, or examples/*/cells/*.
func CellLocationFromRel(rel string) (CellLocation, bool) {
	parts := splitConventionalPath(rel)
	if len(parts) >= 2 && parts[0] == "cells" {
		return CellLocation{ID: parts[1], Kind: CellRootLocal, RootRel: path.Join(parts[0], parts[1])}, true
	}
	if len(parts) >= 2 && parts[0] == "corecells" {
		return CellLocation{ID: parts[1], Kind: CellRootPlatform, RootRel: path.Join(parts[0], parts[1])}, true
	}
	if len(parts) >= 4 && parts[0] == "examples" && parts[2] == "cells" {
		return CellLocation{
			ID:       parts[3],
			Kind:     CellRootExample,
			RootRel:  path.Join(parts[0], parts[1], parts[2], parts[3]),
			ModuleID: parts[1],
		}, true
	}
	return CellLocation{}, false
}

// CellIDFromRel returns the cell ID for a module-relative path under a
// supported cell layout.
func CellIDFromRel(rel string) (string, bool) {
	loc, ok := CellLocationFromRel(rel)
	return loc.ID, ok
}

// CellDirFromMetadataFile returns the cell package directory for a parsed
// cell.yaml path.
func CellDirFromMetadataFile(file string) (string, bool) {
	rel := filepath.ToSlash(file)
	if path.Base(rel) != "cell.yaml" {
		return "", false
	}
	loc, ok := CellLocationFromRel(path.Dir(rel))
	if !ok {
		return "", false
	}
	return loc.RootRel, true
}

// CellImportPrefixes returns import path prefixes that can contain sibling
// cells for the given module path.
func CellImportPrefixes(modulePath string) []string {
	modulePath = strings.TrimSuffix(modulePath, "/")
	if modulePath == "" {
		return nil
	}
	return []string{
		modulePath + "/cells/",
		modulePath + "/corecells/",
		modulePath + "/examples/",
	}
}

// CellIDFromImportPath returns the imported cell ID for a supported cell import
// path rooted at modulePath.
func CellIDFromImportPath(modulePath, importPath string) (string, bool) {
	for _, prefix := range CellImportPrefixes(modulePath) {
		rest, ok := strings.CutPrefix(importPath, prefix)
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		switch {
		case strings.HasSuffix(prefix, "/cells/"), strings.HasSuffix(prefix, "/corecells/"):
			if len(parts) >= 1 && parts[0] != "" {
				return parts[0], true
			}
		case strings.HasSuffix(prefix, "/examples/"):
			if len(parts) >= 3 && parts[1] == "cells" && parts[2] != "" {
				return parts[2], true
			}
		}
	}
	return "", false
}
