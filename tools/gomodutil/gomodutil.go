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
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// ValidateModulePath checks that s is a valid Go module path suitable for use
// as the prefix of generated import paths. It delegates to the canonical
// validator the Go toolchain itself uses (golang.org/x/mod/module) rather than
// a hand-rolled regex — the regex silently admitted malformed paths (trailing
// slash, double slash) that would corrupt the import statements emitted into
// generated files.
//
// It uses CheckImportPath, not CheckPath: the value is joined with the
// module-relative package suffix to form a Go *import path*, and CheckImportPath
// accepts single-segment local/test module paths like "foo" (which CheckPath
// rejects with "missing dot in first path element"). CheckImportPath rejects
// empty strings, "." / ".." path elements, backslashes, whitespace, control
// characters, leading/trailing slashes, and double slashes.
func ValidateModulePath(s string) error {
	// CheckImportPath's error is already self-describing
	// (`malformed import path %q: <reason>`); callers add their own boundary
	// context (e.g. resolveModule prefixes the --module-path flag name).
	return module.CheckImportPath(s)
}

// ReadModulePath reads root/go.mod and returns its declared module path
// (e.g. "github.com/ghbvf/gocell"). It uses golang.org/x/mod/modfile.ModulePath,
// the canonical extractor — robust against leading comments and surrounding
// whitespace, replacing three divergent hand-rolled line-prefix scanners.
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
	if err := ValidateModulePath(mod); err != nil {
		return "", fmt.Errorf("go.mod at %s has invalid module path: %w", root, err)
	}
	return mod, nil
}

// ReadWorkUseDirs reads root/go.work and returns the disk paths of its `use`
// directives, each filepath.Clean'd and kept relative as written (e.g. "." or
// "mdm" or "examples/ssobff"). It uses golang.org/x/mod/modfile.ParseWork -- the
// canonical go.work parser the Go toolchain itself uses -- so it is robust
// against comments, block (`use (...)`) and single-line forms.
//
// go.work `use` is the authoritative set of Go modules the toolchain compiles
// in workspace mode; archtest's workspace enumeration derives its production
// scan set from it (so a module extracted into go.work is auto-covered).
//
// Returns an error when go.work is absent/unreadable or malformed (fail-closed:
// callers must not proceed with a guessed module set). Absolute paths and
// directories that escape the workspace root via ".." are rejected to prevent
// path-traversal bugs (a malicious/buggy go.work use ../../etc would yield a
// Module.Dir that escapes the workspace and flows into LoadProductionPackages).
func ReadWorkUseDirs(root string) ([]string, error) {
	p := filepath.Clean(filepath.Join(root, "go.work"))
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read go.work: %w", err)
	}
	wf, err := modfile.ParseWork(p, data, nil)
	if err != nil {
		return nil, fmt.Errorf("parse go.work at %s: %w", root, err)
	}
	dirs := make([]string, 0, len(wf.Use))
	for _, u := range wf.Use {
		if u == nil || u.Path == "" {
			continue
		}
		cleaned := filepath.Clean(u.Path)
		if filepath.IsAbs(cleaned) {
			return nil, fmt.Errorf(
				"go.work use directive %q resolves to an absolute path %q; only relative paths are allowed",
				u.Path, cleaned,
			)
		}
		for _, seg := range strings.Split(filepath.ToSlash(cleaned), "/") {
			if seg == ".." {
				return nil, fmt.Errorf(
					"go.work use directive %q escapes the workspace root via \"..\"; path traversal is not allowed",
					u.Path,
				)
			}
		}
		dirs = append(dirs, cleaned)
	}
	return dirs, nil
}
