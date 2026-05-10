package scanner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// defaultSkipDirs is the set of directory base-names that are never walked.
var defaultSkipDirs = map[string]struct{}{
	"vendor":       {},
	"testdata":     {},
	"worktrees":    {},
	"generated":    {},
	".git":         {},
	"node_modules": {},
}

// option is an unexported functional-option type. External callers can only
// obtain options via [IncludeTests] and [ExcludeRels]; they cannot define their
// own options. This matches the Go standard-library pattern for sealed option sets.
type option func(*scopeConfig)

type scopeConfig struct {
	includeTests bool
	excludeRels  []string
}

// IncludeTests returns an option that instructs [ModuleScope] and [DirsScope]
// to include *_test.go files in the file set returned by [Scope.Files].
func IncludeTests() option {
	return func(c *scopeConfig) { c.includeTests = true }
}

// ExcludeRels returns an option that excludes specific file paths (relative to
// the module root) from the file set returned by [Scope.Files].
// Paths are matched after filepath.Clean normalization; use slash-separated
// paths on all platforms (e.g., "runtime/auth/roles.go"). Directory exclusion
// is not supported.
// To add custom skip directories, extend the option set in the scanner package;
// callers cannot define new options.
func ExcludeRels(rels ...string) option {
	return func(c *scopeConfig) {
		c.excludeRels = append(c.excludeRels, rels...)
	}
}

// Scope is an opaque file-set descriptor. Obtain a value via [ModuleScope] or
// [DirsScope]; the zero value is invalid and [Scope.Files] will return an error.
type Scope struct {
	// modRoot is the module root used to compute relative paths.
	modRoot string
	// roots are the absolute directory paths to walk.
	roots []string
	// skipDirs contains directory base-names that are skipped during the walk.
	skipDirs map[string]struct{}
	// excludeRels contains relative paths (from modRoot) to exclude.
	excludeRels map[string]struct{}
	// includeTests controls whether _test.go files are included.
	includeTests bool
	// valid is true only when the scope was created by a constructor.
	valid bool
	// escapeErr is set when DirsScope detects dirs that escape modRoot.
	escapeErr error
}

// ModuleScope creates a Scope rooted at modRoot that walks the entire module,
// skipping the default directory set: vendor, testdata, worktrees, generated,
// .git, node_modules.
func ModuleScope(modRoot string, opts ...option) Scope {
	cfg := applyOptions(opts)
	return newScope(modRoot, []string{modRoot}, cfg)
}

// DirsScope creates a Scope limited to dirs (relative to modRoot). Missing
// directories are silently skipped — [Scope.Files] returns an empty slice with
// no error for a scope whose roots do not exist. Dirs that would escape modRoot
// via ".." path traversal are rejected at construction time; [Scope.Files]
// returns an error listing every out-of-bound path.
//
// Prefer DirsScope when the rule applies to specific layers (e.g., runtime/,
// cells/); use [ModuleScope] when the rule must cover the entire repository.
func DirsScope(modRoot string, dirs []string, opts ...option) Scope {
	cfg := applyOptions(opts)
	sep := string(os.PathSeparator)
	cleanMod := filepath.Clean(modRoot)
	roots := make([]string, 0, len(dirs))
	var invalidRoots []string
	for _, d := range dirs {
		abs := filepath.Clean(filepath.Join(cleanMod, d))
		// Accept paths equal to modRoot or strictly under it.
		if abs != cleanMod && !strings.HasPrefix(abs, cleanMod+sep) {
			invalidRoots = append(invalidRoots, d)
			continue
		}
		roots = append(roots, abs)
	}
	s := newScope(modRoot, roots, cfg)
	if len(invalidRoots) > 0 {
		s.escapeErr = fmt.Errorf("DirsScope: dir %q escapes module root", invalidRoots[0])
	}
	return s
}

func applyOptions(opts []option) scopeConfig {
	var cfg scopeConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

func newScope(modRoot string, roots []string, cfg scopeConfig) Scope {
	excludeRels := make(map[string]struct{}, len(cfg.excludeRels))
	for _, r := range cfg.excludeRels {
		// Normalise to OS-native separators for reliable comparison.
		excludeRels[filepath.Clean(r)] = struct{}{}
	}
	return Scope{
		modRoot:      modRoot,
		roots:        roots,
		skipDirs:     defaultSkipDirs,
		excludeRels:  excludeRels,
		includeTests: cfg.includeTests,
		valid:        true,
	}
}

// selfProtectRel is the rel-path prefix of the scanner package itself.
// Files under this prefix are always excluded to prevent self-scanning.
var selfProtectRel = filepath.Join("tools", "archtest", "internal", "scanner")

// Files returns the sorted, deduplicated list of absolute file paths in the
// scope. It returns an error if the scope was not constructed via a constructor
// or if any walk operation fails.
func (s Scope) Files() ([]string, error) {
	if !s.valid {
		return nil, errors.New("scanner: Scope zero value is invalid; use ModuleScope or DirsScope")
	}
	if s.escapeErr != nil {
		return nil, s.escapeErr
	}
	seen := make(map[string]struct{})
	var files []string
	for _, root := range s.roots {
		walked, err := walkGoFiles(s.modRoot, root, s.skipDirs, s.includeTests)
		if err != nil {
			return nil, err
		}
		for _, f := range walked {
			if err := s.collectFile(f, seen, &files); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// contentFiles returns the sorted, deduplicated list of absolute file paths
// in the scope whose path ends in any of suffixes. It mirrors [Scope.Files]
// but with a content-suffix predicate instead of the .go filter, and is the
// internal primitive backing [EachContentFile].
func (s Scope) contentFiles(suffixes []string) ([]string, error) {
	if !s.valid {
		return nil, errors.New("scanner: Scope zero value is invalid; use ModuleScope or DirsScope")
	}
	if s.escapeErr != nil {
		return nil, s.escapeErr
	}
	accept := func(p string) bool { return matchesSuffix(p, suffixes) }
	seen := make(map[string]struct{})
	var files []string
	for _, root := range s.roots {
		walked, err := walkFiles(s.modRoot, root, s.skipDirs, accept)
		if err != nil {
			return nil, err
		}
		for _, f := range walked {
			if err := s.collectFile(f, seen, &files); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// collectFile adds f to files if it passes all exclusion filters.
func (s Scope) collectFile(f string, seen map[string]struct{}, files *[]string) error {
	rel, err := filepath.Rel(s.modRoot, f)
	if err != nil {
		return err
	}
	rel = filepath.Clean(rel)
	// Fail-closed for paths that escaped modRoot (e.g. "../sibling").
	if strings.HasPrefix(filepath.ToSlash(rel), "../") {
		return nil
	}
	if _, excluded := s.excludeRels[rel]; excluded {
		return nil
	}
	// Path-segment boundary match (not bare HasPrefix) so "scanner_extra/"
	// or other prefix-colliding siblings are not falsely excluded.
	// ref: golangci-lint pkg/golinters/depguard — segment-boundary path match
	if rel == selfProtectRel ||
		strings.HasPrefix(rel, selfProtectRel+string(filepath.Separator)) {
		return nil
	}
	if _, dup := seen[f]; !dup {
		seen[f] = struct{}{}
		*files = append(*files, f)
	}
	return nil
}
