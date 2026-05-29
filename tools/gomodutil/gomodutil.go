// Package gomodutil is the single shared go.mod module-path reader used by
// codegen, scaffold, the gocell CLI, and archtest. It replaces three divergent
// hand-rolled scanners (cellgen, cmd/gocell, archtest) with one robust parser.
//
// It returns plain errors (fmt.Errorf) so low-level build-phase callers stay
// free of the pkg/errcode dependency; callers that need a typed error wrap at
// their own boundary.
package gomodutil

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// ReadModulePath reads root/go.mod and returns its declared module path
// (e.g. "github.com/ghbvf/gocell"). It uses golang.org/x/mod/modfile.ModulePath,
// the canonical extractor — robust against the multi-line `module (...)` block
// form, comments, and quoted paths that the previous line-prefix scanners missed.
//
// Returns an error when go.mod is absent/unreadable or has no module directive
// (fail-closed: callers must not proceed with an empty module path).
func ReadModulePath(root string) (string, error) {
	p := filepath.Clean(filepath.Join(root, "go.mod"))
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	mod := modfile.ModulePath(data)
	if mod == "" {
		return "", fmt.Errorf("go.mod at %s has no module directive", root)
	}
	return mod, nil
}
