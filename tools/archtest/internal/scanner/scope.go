package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirsScopeEscapeError is the structured error returned via [Scope.Files] when
// one or more directories supplied to [DirsScope] would resolve outside the
// module root. Callers verify this condition with errors.As and inspect Dirs;
// keep the field exported so tests can assert on the offending paths without
// resorting to substring matching on the message.
type DirsScopeEscapeError struct {
	// Dirs are the offending input directories, preserved in their original
	// (caller-supplied, pre-clean) form so error messages stay meaningful.
	Dirs []string
}

func (e *DirsScopeEscapeError) Error() string {
	return "DirsScope: dirs escape module root: " + strings.Join(e.Dirs, ", ")
}

// defaultSkipDirs is the set of directory base-names that are never walked.
var defaultSkipDirs = map[string]struct{}{
	"vendor":       {},
	"testdata":     {},
	"worktrees":    {},
	"generated":    {},
	".git":         {},
	"node_modules": {},
}

// Option is the functional-option type accepted by [ModuleScope] and
// [DirsScope]. The underlying [scopeConfig] is unexported, so external callers
// can only obtain Options via the exported [IncludeTests] / [ExcludeRels] /
// [MatchRels] / [IncludeTestdata] / [IncludeGenerated] constructors — they
// cannot author new Option values. This matches the Go standard-library
// pattern for sealed option sets.
type Option func(*scopeConfig)

type scopeConfig struct {
	includeTests     bool
	excludeRels      []string
	matchRel         func(rel string) bool
	includeTestdata  bool
	includeGenerated bool
}

// IncludeTests returns an option that instructs [ModuleScope] and [DirsScope]
// to include *_test.go files in the file set returned by [Scope.Files].
func IncludeTests() Option {
	return func(c *scopeConfig) { c.includeTests = true }
}

// ExcludeRels returns an option that excludes specific file paths (relative to
// the module root) from the file set returned by [Scope.Files].
// Paths are matched after filepath.Clean normalization; use slash-separated
// paths on all platforms (e.g., "runtime/auth/roles.go"). Directory exclusion
// is not supported.
// To add custom skip directories, extend the option set in the scanner package;
// callers cannot define new options.
func ExcludeRels(rels ...string) Option {
	return func(c *scopeConfig) {
		c.excludeRels = append(c.excludeRels, rels...)
	}
}

// MatchRels returns an option that retains only files whose module-relative
// slash path satisfies pred. Applied AFTER default skip + ExcludeRels (composes
// AND with both — file must satisfy MatchRels AND not be in ExcludeRels).
//
// Use for glob-style patterns that filename-suffix alone cannot express:
//
//	MatchRels(func(rel string) bool { return filepath.Base(rel) == "cell.yaml" })
//	MatchRels(func(rel string) bool { return strings.HasPrefix(filepath.Base(rel), "relay") })
//
// rel is in slash form on all platforms. Multiple MatchRels options are
// chained (all predicates must return true). A nil predicate is silently
// ignored.
func MatchRels(pred func(rel string) bool) Option {
	return func(c *scopeConfig) {
		if pred == nil {
			return
		}
		if c.matchRel == nil {
			c.matchRel = pred
			return
		}
		prev := c.matchRel
		c.matchRel = func(rel string) bool { return prev(rel) && pred(rel) }
	}
}

// IncludeTestdata returns an option that allows the walk to descend into
// directories named "testdata" (which are otherwise excluded by the default
// skip set). Legal only when the scope's roots include at least one path
// with a "testdata" segment relative to the module root; otherwise
// [Scope.Files] returns an error. ModuleScope + IncludeTestdata always
// errors — there is no legitimate use case for module-wide testdata
// scanning, and that is precisely the regression the default skip prevents.
func IncludeTestdata() Option {
	return func(c *scopeConfig) { c.includeTestdata = true }
}

// IncludeGenerated returns an option that allows the walk to descend into
// directories named "generated" (which are otherwise excluded by the default
// skip set). Use for "anywhere in the module" semantics where generated code
// must also be subject to the rule — e.g. anti-regression rules whose
// invariant would be defeated if codegen reintroduced the forbidden symbol.
//
// Unlike IncludeTestdata, no path-segment validation is required: any rule
// that legitimately wants module-wide coverage including codegen output is
// the use case. Combine with [ModuleScope] for repo-wide "anywhere" rules.
func IncludeGenerated() Option {
	return func(c *scopeConfig) { c.includeGenerated = true }
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
	// matchRel, when non-nil, filters files to those for which it returns true.
	matchRel func(rel string) bool
	// includeTests controls whether _test.go files are included.
	includeTests bool
	// valid is true only when the scope was created by a constructor.
	valid bool
	// setupErr is set when scope construction violates a contract
	// (DirsScope dirs escape modRoot, ModuleScope+IncludeTestdata, etc.).
	setupErr error
}

// ModuleScope creates a Scope rooted at modRoot that walks the entire module,
// skipping the default directory set: vendor, testdata, worktrees, generated,
// .git, node_modules.
func ModuleScope(modRoot string, opts ...Option) Scope {
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
func DirsScope(modRoot string, dirs []string, opts ...Option) Scope {
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
		s.setupErr = &DirsScopeEscapeError{Dirs: invalidRoots}
	}
	return s
}

func applyOptions(opts []Option) scopeConfig {
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
	s := Scope{
		modRoot:      modRoot,
		roots:        roots,
		skipDirs:     buildSkipDirs(cfg),
		excludeRels:  excludeRels,
		matchRel:     cfg.matchRel,
		includeTests: cfg.includeTests,
		valid:        true,
	}
	if cfg.includeTestdata && !rootsContainTestdataSegment(modRoot, roots) {
		s.setupErr = errors.New(`scanner: IncludeTestdata requires DirsScope dirs to contain a "testdata" path ` +
			`segment; ModuleScope and dirs without testdata are rejected`)
	}
	return s
}

// buildSkipDirs returns the directory-name skip set for this scope. When
// IncludeTestdata is set, "testdata" is removed from the default set; when
// IncludeGenerated is set, "generated" is removed. Other entries
// (vendor / worktrees / .git / node_modules) are never opt-in-able.
func buildSkipDirs(cfg scopeConfig) map[string]struct{} {
	if !cfg.includeTestdata && !cfg.includeGenerated {
		return defaultSkipDirs
	}
	out := make(map[string]struct{}, len(defaultSkipDirs))
	for k := range defaultSkipDirs {
		if cfg.includeTestdata && k == "testdata" {
			continue
		}
		if cfg.includeGenerated && k == "generated" {
			continue
		}
		out[k] = struct{}{}
	}
	return out
}

// rootsContainTestdataSegment reports whether at least one root, after being
// expressed module-relative, contains a "testdata" path segment. Used to
// validate that IncludeTestdata is not used to scan modRoot wholesale.
func rootsContainTestdataSegment(modRoot string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(modRoot, root)
		if err != nil {
			continue
		}
		for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
			if seg == "testdata" {
				return true
			}
		}
	}
	return false
}

// selfProtectRel is the rel-path prefix of the scanner package itself.
// Files under this prefix are always excluded to prevent self-scanning.
var selfProtectRel = filepath.Join("tools", "archtest", "internal", "scanner")

// Files returns the sorted, deduplicated list of absolute file paths in the
// scope. It returns an error if the scope was not constructed via a constructor
// or if any walk operation fails.
func (s Scope) Files() ([]string, error) {
	return s.collect(func(p string) bool { return isGoFile(p, s.includeTests) })
}

// ModRoot returns the module root (absolute, OS-native) the scope was
// constructed against. Returns the empty string for a zero-value Scope.
// Callers use this to derive module-relative paths for files discovered by
// [Scope.Files] without re-running the walk.
func (s Scope) ModRoot() string {
	return s.modRoot
}

// contentFiles returns the sorted, deduplicated list of absolute file paths
// in the scope whose path ends in any of suffixes. It mirrors [Scope.Files]
// but with a content-suffix predicate instead of the .go filter, and is the
// internal primitive backing [LoadContentFiles] (which in turn backs
// [EachContentFile]).
func (s Scope) contentFiles(suffixes []string) ([]string, error) {
	return s.collect(func(p string) bool { return matchesSuffix(p, suffixes) })
}

// collect is the shared backbone of [Scope.Files] and [Scope.contentFiles]:
// validates the scope, walks every root with accept as the per-file predicate,
// applies the exclusion chain via collectFile, deduplicates and sorts.
func (s Scope) collect(accept func(string) bool) ([]string, error) {
	if !s.valid {
		return nil, errors.New("scanner: Scope zero value is invalid; use ModuleScope or DirsScope")
	}
	if s.setupErr != nil {
		return nil, s.setupErr
	}
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
	if s.matchRel != nil && !s.matchRel(filepath.ToSlash(rel)) {
		return nil
	}
	if _, dup := seen[f]; !dup {
		seen[f] = struct{}{}
		*files = append(*files, f)
	}
	return nil
}
