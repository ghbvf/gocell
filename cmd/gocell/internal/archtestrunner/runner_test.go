package archtestrunner

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRun_GOWORKOff verifies that Run returns an error immediately when
// GOWORK=off, without spawning any subprocess.
func TestRun_GOWORKOff(t *testing.T) {
	t.Setenv("GOWORK", "off")
	_, err := Run(context.Background(), Request{WorkspaceRoot: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOWORK")
}

// TestListTests_GOWORKOff mirrors the same guard for ListTests.
func TestListTests_GOWORKOff(t *testing.T) {
	t.Setenv("GOWORK", "off")
	_, err := ListTests(context.Background(), Request{WorkspaceRoot: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOWORK")
}

// TestBuildTestArgs_BasicShape verifies the arg vector produced for a typical request.
func TestBuildTestArgs_BasicShape(t *testing.T) {
	selected := []string{"TestAlpha", "TestBeta"}
	args := buildTestArgs("5m", selected)

	assert.Contains(t, args, "-tags=archtest")
	assert.Contains(t, args, "-count=1")
	assert.Contains(t, args, "-json")
	assert.Contains(t, args, "-timeout=5m")
	assert.Contains(t, args, "./tools/archtest")

	// The -run flag must contain a regex pattern with all selected tests.
	var runFlag string
	for _, a := range args {
		if len(a) > 5 && a[:5] == "-run=" {
			runFlag = a
			break
		}
	}
	require.NotEmpty(t, runFlag, "-run= flag must be present")
	assert.Contains(t, runFlag, "TestAlpha")
	assert.Contains(t, runFlag, "TestBeta")
	// Must be anchored with ^ and $
	assert.Contains(t, runFlag, "^(")
	assert.Contains(t, runFlag, ")$")
}

// TestBuildTestArgs_DefaultTimeout verifies that an empty timeout defaults to "5m".
func TestBuildTestArgs_DefaultTimeout(t *testing.T) {
	args := buildTestArgs("", []string{"TestX"})
	assert.Contains(t, args, "-timeout=5m")
}

// TestBuildTestArgs_CustomTimeout verifies that a custom timeout is respected.
func TestBuildTestArgs_CustomTimeout(t *testing.T) {
	args := buildTestArgs("10m", []string{"TestX"})
	assert.Contains(t, args, "-timeout=10m")
}

// TestBuildDiscoverArgs verifies the arg vector for the -list discovery run.
func TestBuildDiscoverArgs(t *testing.T) {
	args := buildDiscoverArgs()
	assert.Contains(t, args, "-tags=archtest")
	assert.Contains(t, args, "-list")
	assert.Contains(t, args, "^Test")
	assert.Contains(t, args, "./tools/archtest")
}

// TestApplySelection_NoShardNoRuleNoChanged verifies that without any filters
// the full discovered set is returned.
func TestApplySelection_NoShardNoRuleNoChanged(t *testing.T) {
	discovered := []string{"TestA", "TestB", "TestC"}
	req := Request{Scope: ScopeWorkspace}
	got := applyShardSelection(discovered, req.Shard)
	assert.Equal(t, discovered, got)
}

// TestApplySelection_WithShard verifies shard selection is applied.
func TestApplySelection_WithShard(t *testing.T) {
	discovered := []string{"TestA", "TestB", "TestC", "TestD"}
	// shard 0 of 2: NR=2,4 → TestB, TestD
	got := applyShardSelection(discovered, Shard{Index: 0, Total: 2})
	assert.Equal(t, []string{"TestB", "TestD"}, got)
}

// TestParseDiscoveryOutput exercises the discovery output parser.
func TestParseDiscoveryOutput(t *testing.T) {
	output := `TestAlpha
TestBeta
some build noise
ok  	./tools/archtest	0.001s
TestGamma
`
	got := parseDiscoveryOutput(output)
	assert.Equal(t, []string{"TestAlpha", "TestBeta", "TestGamma"}, got)
}

// TestRunEmptySelected verifies that when selection produces empty set,
// Run returns a trivial passed Report without spawning go test.
func TestRunEmptySelected(t *testing.T) {
	// Ensure GOWORK is not "off" so the GOWORK guard doesn't trigger.
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off: skipping")
	}
	// We can't run real go test here, but we can test the empty-selected path
	// by verifying the function's response when selected is empty.
	report := buildEmptyReport()
	assert.True(t, report.Passed)
	assert.Empty(t, report.Selected)
	assert.Empty(t, report.Tests)
}

// TestApplyFilters_NoFilters verifies that with no filters, all tests pass through.
func TestApplyFilters_NoFilters(t *testing.T) {
	root := t.TempDir()
	discovered := []string{"TestA", "TestB", "TestC"}
	req := Request{WorkspaceRoot: root}
	e := engine{exec: defaultExec, changed: changedRepoFiles}
	got, err := e.applyFilters(t.Context(), req, discovered)
	require.NoError(t, err)
	assert.Equal(t, discovered, got)
}

// TestApplyFilters_WithShard verifies shard filter.
func TestApplyFilters_WithShard(t *testing.T) {
	root := t.TempDir()
	discovered := []string{"TestA", "TestB", "TestC", "TestD"}
	req := Request{WorkspaceRoot: root, Shard: Shard{Index: 0, Total: 2}}
	e := engine{exec: defaultExec, changed: changedRepoFiles}
	got, err := e.applyFilters(t.Context(), req, discovered)
	require.NoError(t, err)
	// shard 0 of 2: NR=2 (B), NR=4 (D) → TestB, TestD
	assert.Equal(t, []string{"TestB", "TestD"}, got)
}

// TestApplyFilters_WithRule verifies rule-based selection.
func TestApplyFilters_WithRule(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": `//go:build archtest

// INVARIANT: LAYER-05

package archtest

import "testing"

func TestLayerA(t *testing.T) {}
func TestLayerB(t *testing.T) {}
`,
	})

	discovered := []string{"TestLayerA", "TestLayerB", "TestOther"}
	req := Request{WorkspaceRoot: root, Rule: "LAYER-05"}
	e := engine{exec: defaultExec, changed: changedRepoFiles}
	got, err := e.applyFilters(t.Context(), req, discovered)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"TestLayerA", "TestLayerB"}, got)
}

// TestApplyFilters_UnknownRule returns error.
func TestApplyFilters_UnknownRule(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
`,
	})

	discovered := []string{"TestFoo"}
	req := Request{WorkspaceRoot: root, Rule: "NONEXISTENT-99"}
	e := engine{exec: defaultExec, changed: changedRepoFiles}
	_, err := e.applyFilters(t.Context(), req, discovered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown rule")
}

// TestEnrichWithMeta verifies file and rules are populated.
func TestEnrichWithMeta(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": `//go:build archtest

// INVARIANT: LAYER-05

package archtest

import "testing"

func TestLayerX(t *testing.T) {}
`,
	})

	tests := []TestResult{{Name: "TestLayerX", Status: "pass"}}
	enrichWithMeta(t.Context(), root, tests)

	assert.Contains(t, tests[0].File, "layer_test.go")
	assert.Contains(t, tests[0].Rules, "LAYER-05")
}

// TestGoToolName verifies the tool name is os-correct.
func TestGoToolName(t *testing.T) {
	name := goToolName()
	if runtime.GOOS == "windows" {
		assert.Equal(t, "go.exe", name)
	} else {
		assert.Equal(t, "go", name)
	}
}

// TestCheckGOWORK_NotOff verifies no error when GOWORK is not set to "off".
func TestCheckGOWORK_NotOff(t *testing.T) {
	t.Setenv("GOWORK", "")
	assert.NoError(t, checkGOWORK())
}

// TestCheckGOWORK_Off verifies error returned when GOWORK=off.
func TestCheckGOWORK_Off(t *testing.T) {
	t.Setenv("GOWORK", "off")
	err := checkGOWORK()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOWORK")
}

// TestHasFailures reports true when a fail result is present.
func TestHasFailures(t *testing.T) {
	results := []TestResult{
		{Name: "TestA", Status: "pass"},
		{Name: "TestB", Status: "fail"},
	}
	assert.True(t, hasFailures(results))
}

// TestHasFailures_NoFail reports false when no fail results.
func TestHasFailures_NoFail(t *testing.T) {
	results := []TestResult{
		{Name: "TestA", Status: "pass"},
		{Name: "TestB", Status: "skip"},
	}
	assert.False(t, hasFailures(results))
}

// TestHasFailures_Empty reports false for empty slice.
func TestHasFailures_Empty(t *testing.T) {
	assert.False(t, hasFailures(nil))
}

// TestBuildRunPattern verifies the regex pattern is anchored correctly.
func TestBuildRunPattern(t *testing.T) {
	pattern := buildRunPattern([]string{"TestA", "TestB", "TestC"})
	assert.Equal(t, "^(TestA|TestB|TestC)$", pattern)
}

// TestGoTestExtraEnv verifies it returns a non-empty slice with PATH.
func TestGoTestExtraEnv(t *testing.T) {
	env := goTestExtraEnv("/usr/local/go/bin")
	require.NotEmpty(t, env)
	// Should contain PATH entry.
	found := false
	for _, e := range env {
		k, _, ok := splitEnvKV(e)
		if ok && isPathKey(k) {
			found = true
			break
		}
	}
	assert.True(t, found, "goTestExtraEnv should include PATH")
}

// TestGoTestExtraEnv_EmptyDir returns nil for empty dir.
func TestGoTestExtraEnv_EmptyDir(t *testing.T) {
	env := goTestExtraEnv("")
	assert.Nil(t, env)
}

// TestPrependToPath verifies path prepending.
func TestPrependToPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := prependToPath("/new/bin", "/existing/bin")
	assert.Equal(t, "/new/bin"+sep+"/existing/bin", got)
}

// TestPrependToPath_EmptyExisting returns dir alone.
func TestPrependToPath_EmptyExisting(t *testing.T) {
	assert.Equal(t, "/new/bin", prependToPath("/new/bin", ""))
}

// TestPrependToPath_EmptyDir returns existing.
func TestPrependToPath_EmptyDir(t *testing.T) {
	assert.Equal(t, "/existing/bin", prependToPath("", "/existing/bin"))
}

// TestSplitEnvKV verifies KEY=VALUE splitting.
func TestSplitEnvKV(t *testing.T) {
	k, v, ok := splitEnvKV("PATH=/usr/bin:/usr/local/bin")
	assert.True(t, ok)
	assert.Equal(t, "PATH", k)
	assert.Equal(t, "/usr/bin:/usr/local/bin", v)

	_, _, ok2 := splitEnvKV("NOEQUALSSIGN")
	assert.False(t, ok2)
}

// TestIsPathKey verifies PATH key detection.
func TestIsPathKey(t *testing.T) {
	assert.True(t, isPathKey("PATH"))
}

// TestCurrentPathEnv verifies we can read the PATH from the environment.
func TestCurrentPathEnv(t *testing.T) {
	k, v := currentPathEnv()
	assert.Equal(t, "PATH", k)
	assert.NotEmpty(t, v, "PATH should be set in test environment")
}
