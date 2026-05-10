package typeseval_test

import (
	"bufio"
	"fmt"
	"go/build/constraint"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// platformTagAllowlist are tags handled by the Go toolchain itself and do
// not represent project-specific gates that archtest rules must iterate.
var platformTagAllowlist = map[string]bool{
	// OS
	"darwin": true, "linux": true, "windows": true, "freebsd": true,
	"openbsd": true, "netbsd": true, "android": true, "ios": true,
	"plan9": true, "solaris": true, "aix": true, "dragonfly": true,
	"js": true, "wasip1": true,
	// Architecture
	"amd64": true, "arm64": true, "386": true, "arm": true,
	"ppc64": true, "ppc64le": true, "mips": true, "mipsle": true,
	"mips64": true, "mips64le": true, "riscv64": true, "s390x": true,
	"loong64": true, "wasm": true,
	// Build
	"cgo": true, "ignore": true, "race": true, "msan": true, "asan": true,
	"unix": true, "boringcrypto": true,
	// Synthetic non-existent tags only used as "skip this file" markers
	// (the file is intentionally excluded from every real build set).
	"never": true,
}

// isGoVersionTag matches tags like "go1.18", "go1.21", etc.
func isGoVersionTag(s string) bool {
	return strings.HasPrefix(s, "go1.")
}

// repoSkipDirs lists top-level directories that walkBuildTagFiles must NOT
// descend into. These contain fixtures, generated code, vendored deps,
// VCS metadata, or worktree alternates — none of which gate production
// behaviour.
var repoSkipDirs = map[string]bool{
	"vendor":       true,
	".git":         true,
	"generated":    true,
	"testdata":     true,
	"worktrees":    true,
	"node_modules": true,
}

// TestKnownNonDefaultTagsCoverage is a fail-closed self-test: it walks every
// .go file in the repo (production and test), parses any `//go:build`
// directive, and asserts every referenced tag is either a Go-toolchain
// platform tag (in platformTagAllowlist) or appears in
// typeseval.FlatNonDefaultTags(). A new build tag introduced anywhere
// under the module without a corresponding KnownNonDefaultTags() update
// makes this test fail.
//
// Closes PR445-FU finding F2's drift risk: archtest rules that iterate
// build tag combinations (svctoken_caller_cell, test_time_literal, future
// rules) all read from the same single source and are guaranteed not to
// silently miss a newly-introduced tag.
func TestKnownNonDefaultTagsCoverage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	known := map[string]bool{}
	for _, tag := range typeseval.FlatNonDefaultTags() {
		known[tag] = true
	}

	// Collect every tag-identifier appearing in a //go:build directive,
	// alongside the file path (kept for diagnostic output on failure).
	type seenEntry struct {
		paths []string
	}
	seen := map[string]*seenEntry{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && repoSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		tags, perr := extractBuildTags(path)
		if perr != nil {
			return perr
		}
		for _, tag := range tags {
			if _, ok := seen[tag]; !ok {
				seen[tag] = &seenEntry{}
			}
			rel, _ := filepath.Rel(root, path)
			seen[tag].paths = append(seen[tag].paths, rel)
		}
		return nil
	})
	require.NoError(t, err, "filepath.WalkDir")

	var unknown []string
	for tag, entry := range seen {
		if known[tag] || platformTagAllowlist[tag] || isGoVersionTag(tag) {
			continue
		}
		example := entry.paths[0]
		unknown = append(unknown, fmt.Sprintf("%q (first seen at %s)", tag, example))
	}
	sort.Strings(unknown)

	require.Empty(t, unknown,
		"build tags referenced in //go:build directives but missing from "+
			"typeseval.KnownNonDefaultTags() / platformTagAllowlist: %v.\n"+
			"Add the new tag combination to KnownNonDefaultTags() so archtest "+
			"rules that iterate tag-sets (svctoken_caller_cell, test_time_literal, "+
			"etc.) load the gated files instead of silently skipping them.",
		unknown)
}

// extractBuildTags reads path's first comment block and returns every tag
// identifier referenced in any //go:build directive. Returns nil if no
// directive present.
func extractBuildTags(path string) ([]string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var tags []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			// Blank line separates the build-constraint comment block from
			// the package clause; per Go spec, //go:build must precede the
			// blank line. We can stop scanning.
			break
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
		if !strings.HasPrefix(line, "//go:build") {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			continue
		}
		// Walk the expression to enumerate tag identifiers.
		walkConstraintTags(expr, &tags)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return tags, nil
}

// walkConstraintTags appends every TagExpr's Tag to *out, recursing through
// And/Or/Not nodes.
func walkConstraintTags(e constraint.Expr, out *[]string) {
	switch v := e.(type) {
	case *constraint.AndExpr:
		walkConstraintTags(v.X, out)
		walkConstraintTags(v.Y, out)
	case *constraint.OrExpr:
		walkConstraintTags(v.X, out)
		walkConstraintTags(v.Y, out)
	case *constraint.NotExpr:
		walkConstraintTags(v.X, out)
	case *constraint.TagExpr:
		*out = append(*out, v.Tag)
	}
}

// repoRoot returns the module root by walking up from the test binary's
// working directory until it finds a go.mod file.
func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	d := cwd
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatalf("repoRoot: go.mod not found above %s", cwd)
		}
		d = parent
	}
}
