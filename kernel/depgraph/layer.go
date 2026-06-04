package depgraph

import (
	"sort"
	"strings"
)

// Layer constants name the buckets used by archtest layering rules and the
// `gocell graph` CLI. They are the JSON values for Node.Layer.
const (
	LayerKernel   = "kernel"
	LayerRuntime  = "runtime"
	LayerAdapters = "adapters"
	LayerCells    = "cells"
	LayerPkg      = "pkg"
	LayerCmd      = "cmd"
	// LayerCellModules is the composition-root layer for cellmodules/<cell> modules.
	// It wires platform Cell modules by importing cells/ + adapters/ + runtime/
	// and is the importable counterpart to cmd/ (which is an unimportable CLI
	// binary). Unlike cmd/, cellmodules/ packages are designed to be imported by
	// examples/ and external composition roots (#1085 CellModule public API).
	LayerCellModules = "cellmodules"
	LayerExamples    = "examples"
	LayerTools       = "tools"
	LayerTests       = "tests"
	LayerGenerated   = "generated"
	LayerRoot        = "root"
	LayerStdlib      = "stdlib"
	LayerThirdParty  = "thirdparty"
	// LayerUnknown marks a package whose import path is module-internal
	// (lives under the module root) but whose first segment is not in
	// internalLayerByDir. This is distinct from LayerThirdParty so that
	// governance code can detect a new top-level directory that needs
	// classification, instead of silently treating it as external.
	LayerUnknown = "unknown"
)

// internalLayerByDir maps a top-level directory under the module root to
// its Layer. Unrecognized segments are reported as LayerUnknown — see
// Classifier.Layer for the failure-loud rationale.
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
	// cellmodules/ is the importable Composition Root layer (#1085): it wires
	// platform Cell modules (accesscore / auditcore / configcore) by importing
	// cells/ + adapters/ + runtime/, and exposes Module() constructors for
	// external composition roots (cmd/corebundle, examples/corebundlestarter).
	// Unlike cmd/ (an unimportable CLI binary), cellmodules/ is an importable
	// library. Uses LayerCellModules (not LayerCmd) so governance rules can
	// distinguish between the two composition-root forms.
	"cellmodules": LayerCellModules,
}

// Classifier maps Go import paths to layers / cell IDs / slice IDs relative to
// a SET of workspace module paths. It is the single classification entry point;
// there is no module-singular free function, because a caller that passed the
// core module path for a nested module's package would mis-bucket it as
// LayerUnknown.
//
// OwningModule selects the module that owns an import path by LONGEST matching
// prefix, so a nested module path (e.g. "github.com/ghbvf/gocell/mdm") wins over
// the core module ("github.com/ghbvf/gocell") for a package like
// ".../mdm/cells/foo" — which then classifies as LayerCells within the mdm
// module rather than LayerUnknown under the core module.
//
// A single-module caller constructs NewClassifier([]string{module}); behavior is
// byte-identical to the former module-singular classification. The multi-module
// workspace passes every member module's import path.
type Classifier struct {
	// modules is sorted by descending length so OwningModule's first match is
	// the longest (most specific) owning prefix.
	modules []string
}

// NewClassifier builds a Classifier over the given module import paths. The
// input is copied and sorted longest-first (ties broken lexically for
// determinism); the caller's slice is neither retained nor mutated. Empty
// entries are dropped.
func NewClassifier(modules []string) Classifier {
	cp := make([]string, 0, len(modules))
	for _, m := range modules {
		if m != "" {
			cp = append(cp, m)
		}
	}
	sort.Slice(cp, func(i, j int) bool {
		if len(cp[i]) != len(cp[j]) {
			return len(cp[i]) > len(cp[j])
		}
		return cp[i] < cp[j]
	})
	return Classifier{modules: cp}
}

// OwningModule returns the module path that owns importPath (the longest member
// equal to importPath or a "<module>/" prefix of it), or "" when no member owns
// it (stdlib / third-party / unrelated).
func (c Classifier) OwningModule(importPath string) string {
	for _, m := range c.modules {
		if importPath == m || strings.HasPrefix(importPath, m+"/") {
			return m
		}
	}
	return ""
}

// Layer classifies importPath relative to its owning module. Internal-module
// packages map to one of LayerKernel..LayerGenerated based on the first path
// segment under the owning module; the bare owning-module path maps to
// LayerRoot; an internal first segment not in internalLayerByDir maps to
// LayerUnknown — distinct from LayerThirdParty so consumers can spot
// repo-structure evolution that has not been classified.
//
// Packages owned by no member module classify as LayerStdlib (no dot in first
// segment) or LayerThirdParty.
func (c Classifier) Layer(importPath string) string {
	if importPath == "" {
		return ""
	}
	owner := c.OwningModule(importPath)
	if owner == "" {
		if IsStdlib(importPath) {
			return LayerStdlib
		}
		return LayerThirdParty
	}
	if importPath == owner {
		return LayerRoot
	}
	rel := strings.TrimPrefix(importPath, owner+"/")
	seg := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		seg = rel[:i]
	}
	if layer, ok := internalLayerByDir[seg]; ok {
		return layer
	}
	return LayerUnknown
}

// Cell returns the cell ID for a package under <owningModule>/cells/<id>/...,
// or "" if the package is not under any member module's cells/. The Go-reserved
// "internal" segment (e.g. cells/internal/testoutbox — shared cell-test
// helpers) is not a cell ID; Cell returns "" for paths under cells/internal/.
func (c Classifier) Cell(importPath string) string {
	owner := c.OwningModule(importPath)
	if owner == "" {
		return ""
	}
	prefix := owner + "/cells/"
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

// Slice returns the slice ID for a package under
// <owningModule>/cells/<id>/slices/<sliceId>/..., or "" if not under a slice.
// Slices may have nested subdirectories; only the immediate slice ID is
// returned.
func (c Classifier) Slice(importPath string) string {
	cell := c.Cell(importPath)
	if cell == "" {
		return ""
	}
	owner := c.OwningModule(importPath)
	prefix := owner + "/cells/" + cell + "/slices/"
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
//
// In a multi-module workspace, prefer [Classifier.OwningModule](importPath) == ""
// (longest-prefix) over this function — IsThirdParty takes a single module string
// and would mis-classify a satellite module's packages as third-party when the
// satellite path is not passed as the module argument.
func IsThirdParty(module, importPath string) bool {
	if importPath == "" {
		return false
	}
	if importPath == module || strings.HasPrefix(importPath, module+"/") {
		return false
	}
	return !IsStdlib(importPath)
}
