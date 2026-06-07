// Package gomodutil is the single shared go.mod module-path reader used by
// codegen, scaffold, the gocell CLI, and archtest. It replaces three divergent
// hand-rolled scanners (cellgen, cmd/gocell, archtest) with one robust parser.
//
// It returns plain errors (fmt.Errorf) so low-level build-phase callers stay
// free of the pkg/errcode dependency; callers that need a typed error wrap at
// their own boundary.
package gomodutil

import (
	"errors"
	"fmt"
	"io/fs"
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
// callers must not proceed with a guessed module set). Three path-traversal
// guards reject a use directive whose Module.Dir would escape the workspace and
// flow into LoadProductionPackages (`go list ./<dir>/...` scanning packages
// outside the repo): absolute paths, ".." segments, and SYMLINKS that resolve
// outside the root (e.g. `use ./linked` where ./linked -> /outside — which
// string-cleaning alone cannot catch). The symlink guard is existence-agnostic:
// a use dir that does not exist on disk is left as-is (a not-yet-created member
// cannot be a symlink escape), preserving this parser's existence-agnostic
// contract.
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
	// Resolve the workspace root once (it may itself sit under a symlinked path,
	// e.g. macOS /var -> /private/var) so the containment check below compares
	// resolved-against-resolved.
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root %s: %w", root, err)
	}
	dirs := make([]string, 0, len(wf.Use))
	for _, u := range wf.Use {
		if u == nil || u.Path == "" {
			continue
		}
		cleaned, err := validateWorkUseDir(root, rootResolved, u.Path)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, cleaned)
	}
	return dirs, nil
}

// validateWorkUseDir applies the absolute / ".." / symlink-escape guards to a
// single go.work `use` path and returns its cleaned relative form.
func validateWorkUseDir(root, rootResolved, raw string) (string, error) {
	cleaned := filepath.Clean(raw)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf(
			"go.work use directive %q resolves to an absolute path %q; only relative paths are allowed",
			raw, cleaned,
		)
	}
	for _, seg := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if seg == ".." {
			return "", fmt.Errorf(
				"go.work use directive %q escapes the workspace root via \"..\"; path traversal is not allowed",
				raw,
			)
		}
	}
	if err := checkWorkUseDirSymlink(root, rootResolved, raw, cleaned); err != nil {
		return "", err
	}
	return cleaned, nil
}

// checkWorkUseDirSymlink rejects a use dir that, after resolving symlinks,
// escapes the workspace root. It is a no-op for a dir that does not exist on
// disk (existence-agnostic: a non-existent path cannot be a symlink escape, and
// downstream go.mod reads fail closed for genuinely missing members).
func checkWorkUseDirSymlink(root, rootResolved, raw, cleaned string) error {
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, cleaned))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("resolve go.work use directive %q: %w", raw, err)
	}
	if resolved == rootResolved || strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
		return nil
	}
	return fmt.Errorf(
		"go.work use directive %q resolves via symlink to %q which escapes the workspace root %q; "+
			"symlink path traversal is not allowed",
		raw, resolved, rootResolved,
	)
}

// ReplaceDirective is one `replace` directive from a go.mod, rendered for
// human-readable diagnostics (e.g. "github.com/ghbvf/gocell => ../../").
type ReplaceDirective struct {
	Old string // replaced module, "path" or "path@version"
	New string // replacement: a filesystem path, or "path@version"
}

// ExcludeDirective is one `exclude` directive from a go.mod ("path@version").
type ExcludeDirective struct {
	Path    string
	Version string
}

// ReadReplaceExclude reads root/go.mod and returns its replace and exclude
// directives. A module carrying either is NOT cleanly consumable as an external
// dependency: a downstream `go get`/`go build` ignores the directives (so a
// replace pointing at a local path references code the consumer cannot resolve),
// and `go install pkg@version` of any package in the module is rejected outright
// by the toolchain when the module's go.mod contains replace or exclude
// directives.
//
// It uses golang.org/x/mod/modfile (the canonical parser), so it is robust
// against comments and block/single-line forms. Returns an error when go.mod is
// absent/unreadable or malformed (fail-closed: callers must not treat a parse
// failure as "no directives").
func ReadReplaceExclude(root string) ([]ReplaceDirective, []ExcludeDirective, error) {
	p := filepath.Clean(filepath.Join(root, "go.mod"))
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, fmt.Errorf("read go.mod: %w", err)
	}
	mf, err := modfile.Parse(p, data, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("parse go.mod at %s: %w", root, err)
	}
	replaces := make([]ReplaceDirective, 0, len(mf.Replace))
	for _, r := range mf.Replace {
		if r == nil {
			continue
		}
		replaces = append(replaces, ReplaceDirective{
			Old: formatModuleVersion(r.Old),
			New: formatModuleVersion(r.New),
		})
	}
	excludes := make([]ExcludeDirective, 0, len(mf.Exclude))
	for _, e := range mf.Exclude {
		if e == nil {
			continue
		}
		excludes = append(excludes, ExcludeDirective{Path: e.Mod.Path, Version: e.Mod.Version})
	}
	return replaces, excludes, nil
}

// formatModuleVersion renders a module.Version as "path" (a filesystem
// replacement target carries no version) or "path@version".
func formatModuleVersion(mv module.Version) string {
	if mv.Version == "" {
		return mv.Path
	}
	return mv.Path + "@" + mv.Version
}
