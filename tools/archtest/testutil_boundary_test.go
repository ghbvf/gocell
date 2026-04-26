package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayerTestutil enforces LAYER-TESTUTIL:
//
// No production Go file (i.e. NOT *_test.go) outside
// cells/accesscore/internal/testutil/ may import
// cells/accesscore/internal/testutil.
//
// Rationale: testutil is a shared fixture package required across multiple
// test packages within cells/accesscore; it lives outside *_test.go so that
// sibling packages can import it in their own _test.go files (Go's _test.go
// files cannot import from other packages' _test.go files). The Go compiler's
// internal/ guard already limits the import to cells/accesscore/**, but does
// not distinguish production from test files. This rule closes that gap.
func TestLayerTestutil(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	testutilImport := modPath + "/cells/accesscore/internal/testutil"

	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	testutilPkgDir := filepath.Join(root, "cells", "accesscore", "internal", "testutil")

	var violations []string
	for _, f := range allGoFiles {
		// Only check non-test files — _test.go files are the intended consumers.
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		// The testutil package's own implementation files are exempt.
		if filepath.Dir(f) == testutilPkgDir {
			continue
		}
		imports, err := parseImports(f)
		require.NoError(t, err, "failed to parse %s", f)
		for _, imp := range imports {
			if imp == testutilImport {
				rel, _ := filepath.Rel(root, f)
				rel = filepath.ToSlash(rel)
				violations = append(violations,
					fmt.Sprintf("LAYER-TESTUTIL: %s (non-test file) imports %s (only _test.go files may import testutil)", rel, imp))
			}
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s", v)
		}
	}
	assert.Empty(t, violations,
		"production (non-_test.go) files must not import cells/accesscore/internal/testutil; "+
			"it is a test-fixture package — import it only from *_test.go files")
}
