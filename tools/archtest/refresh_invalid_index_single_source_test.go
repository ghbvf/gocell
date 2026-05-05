package archtest

// refresh_invalid_index_single_source_test.go enforces REFRESH-INVALID-INDEX-SINGLE-SOURCE-01:
// the function "DetectInvalidIndexes" must be declared (defined) in exactly one
// production (non-_test.go) Go file across the entire repository:
// adapters/postgres/schema_guard.go.
//
// Callers of DetectInvalidIndexes (e.g. migrator.go, cmd/corebundle/bundle.go)
// are allowed. Only a second *declaration* (func DetectInvalidIndexes ...) would
// violate the rule, which would indicate B8 or future work introducing a
// parallel invalid-index check path outside schema_guard.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleRefreshInvalidIndexSingleSource01 = "REFRESH-INVALID-INDEX-SINGLE-SOURCE-01"

// canonicalInvalidIndexFile is the only file allowed to define DetectInvalidIndexes.
const canonicalInvalidIndexFile = "adapters/postgres/schema_guard.go"

// TestRefreshInvalidIndexSingleSource01 scans all non-test .go files for a
// top-level FuncDecl named "DetectInvalidIndexes" and asserts exactly one
// such declaration exists (in schema_guard.go).
func TestRefreshInvalidIndexSingleSource01(t *testing.T) {
	root := findModuleRoot(t)

	type declarationSite struct {
		rel  string
		line int
	}
	var declarations []declarationSite

	skipDirs := map[string]struct{}{
		"vendor": {}, "worktrees": {}, "testdata": {}, "generated": {}, ".git": {},
	}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
		if parseErr != nil {
			// Skip unparseable files (e.g. generated with build constraints).
			return nil
		}

		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fd.Name.Name != "DetectInvalidIndexes" {
				continue
			}
			// Only top-level function declarations (no receiver).
			if fd.Recv != nil {
				continue
			}
			rel, _ := filepath.Rel(root, path)
			pos := fset.Position(fd.Pos())
			declarations = append(declarations, declarationSite{
				rel:  filepath.ToSlash(rel),
				line: pos.Line,
			})
		}
		return nil
	})
	require.NoError(t, err, "walking repo root")

	if len(declarations) == 0 {
		t.Fatalf("%s: DetectInvalidIndexes not declared anywhere — expected it in %s",
			ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile)
	}

	if len(declarations) > 1 {
		t.Logf("%s: DetectInvalidIndexes declared in %d files (expected 1):", ruleRefreshInvalidIndexSingleSource01, len(declarations))
		for _, d := range declarations {
			t.Logf("  %s:%d", d.rel, d.line)
		}
	}

	assert.Len(t, declarations, 1,
		"%s: DetectInvalidIndexes must be declared in exactly one production file (%s); "+
			"found declarations in %d files — callers are allowed, new parallel definitions are not",
		ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile, len(declarations))

	if len(declarations) == 1 {
		assert.Equal(t, canonicalInvalidIndexFile, declarations[0].rel,
			"%s: DetectInvalidIndexes must be declared in %s, not %s",
			ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile, declarations[0].rel)
	}
}
