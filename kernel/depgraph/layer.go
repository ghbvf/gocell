package depgraph

import "strings"

// Layer constants name the buckets used by archtest layering rules and the
// `gocell graph` CLI. They are the JSON values for Node.Layer.
const (
	LayerKernel     = "kernel"
	LayerRuntime    = "runtime"
	LayerAdapters   = "adapters"
	LayerCells      = "cells"
	LayerPkg        = "pkg"
	LayerCmd        = "cmd"
	LayerExamples   = "examples"
	LayerTools      = "tools"
	LayerTests      = "tests"
	LayerGenerated  = "generated"
	LayerRoot       = "root"
	LayerStdlib     = "stdlib"
	LayerThirdParty = "thirdparty"
	// LayerUnknown marks a package whose import path is module-internal
	// (lives under the module root) but whose first segment is not in
	// internalLayerByDir. This is distinct from LayerThirdParty so that
	// governance code can detect a new top-level directory that needs
	// classification, instead of silently treating it as external.
	LayerUnknown = "unknown"
)

// internalLayerByDir maps a top-level directory under the module root to
// its Layer. Unrecognized segments are reported as LayerUnknown — see
// LayerOf for the failure-loud rationale.
var internalLayerByDir = map[string]string{
	"kernel":    LayerKernel,
	"runtime":   LayerRuntime,
	"adapters":  LayerAdapters,
	"cells":     LayerCells,
	"pkg":       LayerPkg,
	"cmd":       LayerCmd,
	"examples":  LayerExamples,
	"tools":     LayerTools,
	"tests":     LayerTests,
	"generated": LayerGenerated,
}

// LayerOf classifies importPath relative to module. module must be the
// bare module path without trailing slash (e.g. "github.com/ghbvf/gocell").
//
// Internal-module packages map to one of LayerKernel..LayerGenerated based
// on the first path segment. The bare module path itself maps to LayerRoot.
// Internal packages whose first segment is not in internalLayerByDir map
// to LayerUnknown — distinct from LayerThirdParty so consumers can spot
// repo-structure evolution that has not been classified.
//
// External packages classify as LayerStdlib (no dot in first segment) or
// LayerThirdParty (any other domain).
func LayerOf(module, importPath string) string {
	if importPath == "" {
		return ""
	}
	if importPath == module {
		return LayerRoot
	}
	if strings.HasPrefix(importPath, module+"/") {
		rel := strings.TrimPrefix(importPath, module+"/")
		seg := rel
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			seg = rel[:i]
		}
		if layer, ok := internalLayerByDir[seg]; ok {
			return layer
		}
		return LayerUnknown
	}
	if IsStdlib(importPath) {
		return LayerStdlib
	}
	return LayerThirdParty
}

// CellOf returns the cell ID for a package under module/cells/<id>/...,
// or "" if the package is not under cells/. The Go-reserved "internal"
// segment (e.g. cells/internal/testoutbox — shared cell-test helpers) is
// not a cell ID; CellOf returns "" for paths under cells/internal/.
func CellOf(module, importPath string) string {
	prefix := module + "/cells/"
	if !strings.HasPrefix(importPath, prefix) {
		return ""
	}
	rel := strings.TrimPrefix(importPath, prefix)
	if rel == "" {
		return ""
	}
	seg := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		seg = rel[:i]
	}
	if seg == "internal" {
		return ""
	}
	return seg
}

// SliceOf returns the slice ID for a package under
// module/cells/<id>/slices/<sliceId>/..., or "" if not under a slice.
// Slices may have nested subdirectories; only the immediate slice ID is
// returned.
func SliceOf(module, importPath string) string {
	cell := CellOf(module, importPath)
	if cell == "" {
		return ""
	}
	prefix := module + "/cells/" + cell + "/slices/"
	if !strings.HasPrefix(importPath, prefix) {
		return ""
	}
	rel := strings.TrimPrefix(importPath, prefix)
	if rel == "" {
		return ""
	}
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return rel
}

// IsStdlib reports whether importPath is a standard library package.
//
// Heuristic: the first path segment contains no dot. Stdlib paths are
// "fmt", "net/http", "encoding/json"; module paths always contain a domain
// like "github.com" or "golang.org" with a dot.
func IsStdlib(importPath string) bool {
	if importPath == "" {
		return false
	}
	first := importPath
	if i := strings.IndexByte(importPath, '/'); i >= 0 {
		first = importPath[:i]
	}
	return !strings.ContainsRune(first, '.')
}

// IsThirdParty reports whether importPath belongs to neither the given
// module nor stdlib.
func IsThirdParty(module, importPath string) bool {
	if importPath == "" {
		return false
	}
	if importPath == module || strings.HasPrefix(importPath, module+"/") {
		return false
	}
	return !IsStdlib(importPath)
}
