package archtestrunner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeArchtestDir = "tools/archtest"

// makeFakeArchtestDir creates a temporary workspace structure with
// tools/archtest/*_test.go files for rule-index testing.
func makeFakeArchtestDir(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, fakeArchtestDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	return root
}

func TestRuleIndex_SingleInvariant(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
func TestBar(t *testing.T) {}
`,
	})

	idx, err := buildRuleIndex(root)
	require.NoError(t, err)

	tests, ok := idx["FOO-01"]
	require.True(t, ok, "FOO-01 should be indexed")
	assert.ElementsMatch(t, []string{"TestFoo", "TestBar"}, tests)
}

func TestRuleIndex_ListFormAnchors(t *testing.T) {
	// Tests the list-form anchor parsing:
	//   - INVARIANT: BAR-01
	//   - INVARIANT: BAZ-02
	root := makeFakeArchtestDir(t, map[string]string{
		"multi_test.go": `//go:build archtest

// invariants:
//   - INVARIANT: BAR-01
//   - INVARIANT: BAZ-02

package archtest

import "testing"

func TestMultiA(t *testing.T) {}
func TestMultiB(t *testing.T) {}
`,
	})

	idx, err := buildRuleIndex(root)
	require.NoError(t, err)

	barTests, ok := idx["BAR-01"]
	require.True(t, ok, "BAR-01 should be indexed")
	assert.ElementsMatch(t, []string{"TestMultiA", "TestMultiB"}, barTests)

	bazTests, ok := idx["BAZ-02"]
	require.True(t, ok, "BAZ-02 should be indexed")
	assert.ElementsMatch(t, []string{"TestMultiA", "TestMultiB"}, bazTests)
}

func TestRuleIndex_MultipleFiles(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"alpha_test.go": `//go:build archtest

// INVARIANT: ALPHA-01

package archtest

import "testing"

func TestAlpha(t *testing.T) {}
`,
		"beta_test.go": `//go:build archtest

// INVARIANT: BETA-01

package archtest

import "testing"

func TestBeta(t *testing.T) {}
`,
	})

	idx, err := buildRuleIndex(root)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"TestAlpha"}, idx["ALPHA-01"])
	assert.ElementsMatch(t, []string{"TestBeta"}, idx["BETA-01"])
}

func TestRuleIndex_UnknownRule_Error(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
`,
	})

	idx, err := buildRuleIndex(root)
	require.NoError(t, err)

	_, ok := idx["UNKNOWN-99"]
	assert.False(t, ok, "unknown rule should not appear in index")
}

func TestSelectByRule_KnownRule(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": `//go:build archtest

// INVARIANT: LAYER-05

package archtest

import "testing"

func TestLayerA(t *testing.T) {}
func TestLayerB(t *testing.T) {}
func TestLayerC(t *testing.T) {}
`,
	})

	idx, err := buildRuleIndex(root)
	require.NoError(t, err)

	discovered := []string{"TestLayerA", "TestLayerB", "TestLayerC", "TestOther"}
	result, err := selectByRule(idx, "LAYER-05", discovered)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"TestLayerA", "TestLayerB", "TestLayerC"}, result)
}

func TestSelectByRule_UnknownRule_Error(t *testing.T) {
	idx := ruleIndex{"FOO-01": {"TestFoo"}}
	_, err := selectByRule(idx, "UNKNOWN-99", []string{"TestFoo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown rule")
	assert.Contains(t, err.Error(), "UNKNOWN-99")
}

func TestSelectByRule_IntersectsWithDiscovered(t *testing.T) {
	// Rule maps to TestA and TestB, but only TestB is in discovery.
	idx := ruleIndex{"X-01": {"TestA", "TestB"}}
	result, err := selectByRule(idx, "X-01", []string{"TestB", "TestC"})
	require.NoError(t, err)
	assert.Equal(t, []string{"TestB"}, result)
}

// TestBuildRuleIndex_NonexistentDir returns empty index (not an error) when
// the archtest dir doesn't exist.
func TestBuildRuleIndex_NonexistentDir(t *testing.T) {
	root := t.TempDir()
	// No tools/archtest dir created.
	idx, err := buildRuleIndex(root)
	require.NoError(t, err)
	assert.Empty(t, idx)
}

// TestBuildFileMetaMap_NonexistentDir returns empty meta (not an error).
func TestBuildFileMetaMap_NonexistentDir(t *testing.T) {
	root := t.TempDir()
	meta, err := buildFileMetaMap(root)
	require.NoError(t, err)
	assert.Empty(t, meta)
}

// TestExtractTestFuncNames_NoParams skips functions with no parameters.
func TestExtractTestFuncNames_NoParams(t *testing.T) {
	import_ := `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFooWithParam(t *testing.T) {}
func TestNoParam() {}
func helperFn() {}
`
	root := makeFakeArchtestDir(t, map[string]string{"foo_test.go": import_})
	idx, err := buildRuleIndex(root)
	require.NoError(t, err)

	tests := idx["FOO-01"]
	// Only TestFooWithParam should be included (TestNoParam has 0 params).
	assert.Equal(t, []string{"TestFooWithParam"}, tests)
}

func TestBuildFileMetaMap(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": `//go:build archtest

// INVARIANT: LAYER-05
// INVARIANT: LAYER-06

package archtest

import "testing"

func TestLayerFoo(t *testing.T) {}
func TestLayerBar(t *testing.T) {}
`,
	})

	metaMap, err := buildFileMetaMap(root)
	require.NoError(t, err)

	fooMeta, ok := metaMap["TestLayerFoo"]
	require.True(t, ok)
	assert.Contains(t, fooMeta.file, "layer_test.go")
	assert.ElementsMatch(t, []string{"LAYER-05", "LAYER-06"}, fooMeta.rules)

	barMeta, ok := metaMap["TestLayerBar"]
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"LAYER-05", "LAYER-06"}, barMeta.rules)
}
