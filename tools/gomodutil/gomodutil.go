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
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
)

// modulePathRe validates a plausible Go module path. It mirrors the spirit of
// cellgen's modulePathPattern: letters/digits/hyphens/underscores/dots/slashes
// are all valid; backslash, whitespace, control characters, and ".." are not.
// Single-segment paths like "foo" are accepted (valid for local/test modules).
var modulePathRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._\-/]*$`)

// ValidateModulePath checks that s is a plausible Go module path suitable for
// use in generated import paths. It rejects:
//   - empty strings
//   - paths containing ".." (traversal)
//   - paths containing backslash (Windows path separator)
//   - paths containing whitespace or control characters
//   - paths that do not match the module-path character set
func ValidateModulePath(s string) error {
	if s == "" {
		return fmt.Errorf("module path must not be empty")
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("module path must not contain \"..\": %q", s)
	}
	if strings.ContainsRune(s, '\\') {
		return fmt.Errorf("module path must not contain backslash: %q", s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("module path must not contain whitespace or control characters: %q", s)
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return fmt.Errorf("module path must not contain whitespace: %q", s)
		}
	}
	if !modulePathRe.MatchString(s) {
		return fmt.Errorf("module path contains invalid characters: %q", s)
	}
	return nil
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
	return mod, nil
}
