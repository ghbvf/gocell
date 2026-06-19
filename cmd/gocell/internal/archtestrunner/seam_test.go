package archtestrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExec builds an execFn stub that returns listOutput for -list calls and
// execOutput/execErr for all other calls (exec phase).
func fakeExec(listOutput string, execOutput []byte, execErr error) execFn {
	return func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		for _, a := range args {
			if a == "-list" {
				return []byte(listOutput), nil
			}
		}
		return execOutput, execErr
	}
}

// fakeDiscoverExec returns an execFn whose list output contains the provided
// test names (one per line).
func fakeDiscoverExec(names ...string) execFn {
	listOut := strings.Join(names, "\n") + "\n"
	return fakeExec(listOut, nil, nil)
}

// ---- discoverTests via seam ----

func TestDiscoverTests_ReturnsNames(t *testing.T) {
	e := engine{
		exec: func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
			require.Contains(t, args, "-list", "discover should pass -list")
			return []byte("TestAlpha\nTestBeta\nTestGamma\nok  ./tools/archtest\t0.001s\n"), nil
		},
		changed: changedRepoFiles,
	}

	tests, err := e.discoverTests(context.Background(), t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, []string{"TestAlpha", "TestBeta", "TestGamma"}, tests)
}

func TestDiscoverTests_ZeroDiscovered_Error(t *testing.T) {
	e := engine{
		exec: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			return []byte("ok  ./tools/archtest\t0.001s\n"), nil
		},
		changed: changedRepoFiles,
	}

	_, err := e.discoverTests(context.Background(), t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no archtest Test* functions discovered")
}

func TestDiscoverTests_RunError_Error(t *testing.T) {
	e := engine{
		exec: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			return nil, errors.New("go tool not found")
		},
		changed: changedRepoFiles,
	}

	_, err := e.discoverTests(context.Background(), t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discover archtest tests")
}

// ---- execGoTest via seam ----

func TestExecGoTest_PassesTimeoutAndRunFlag(t *testing.T) {
	var capturedArgs []string
	e := engine{
		exec: func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
			capturedArgs = args
			return []byte(`{"Action":"pass","Test":"TestFoo","Elapsed":0.1}`), nil
		},
		changed: changedRepoFiles,
	}

	req := Request{WorkspaceRoot: t.TempDir(), Timeout: "3m"}
	selected := []string{"TestFoo", "TestBar"}
	_, err := e.execGoTest(context.Background(), req, selected)
	require.NoError(t, err)

	assert.Contains(t, capturedArgs, "-timeout=3m")
	assert.Contains(t, capturedArgs, "-json")
	assert.Contains(t, capturedArgs, "-tags=archtest")

	var runFlag string
	for _, a := range capturedArgs {
		if strings.HasPrefix(a, "-run=") {
			runFlag = a
			break
		}
	}
	assert.Contains(t, runFlag, "TestFoo")
	assert.Contains(t, runFlag, "TestBar")
}

// ---- Run via seam: all-pass path ----

func TestRun_AllPass(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": `//go:build archtest

// INVARIANT: LAYER-05

package archtest

import "testing"

func TestLayerA(t *testing.T) {}
`,
	})

	execOut := []byte(
		`{"Action":"run","Test":"TestLayerA"}` + "\n" +
			`{"Action":"pass","Test":"TestLayerA","Elapsed":0.05}` + "\n",
	)
	e := engine{
		exec:    fakeExec("TestLayerA\n", execOut, nil),
		changed: changedRepoFiles,
	}

	report, err := e.run(context.Background(), Request{WorkspaceRoot: root})
	require.NoError(t, err)
	assert.True(t, report.Passed)
	assert.Equal(t, []string{"TestLayerA"}, report.Selected)
	require.Len(t, report.Tests, 1)
	assert.Equal(t, "pass", report.Tests[0].Status)
}

// ---- Run via seam: some-fail path ----

func TestRun_SomeFail(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": `//go:build archtest

// INVARIANT: LAYER-05

package archtest

import "testing"

func TestLayerA(t *testing.T) {}
func TestLayerB(t *testing.T) {}
`,
	})

	execOut := []byte(
		`{"Action":"run","Test":"TestLayerA"}` + "\n" +
			`{"Action":"output","Test":"TestLayerA","Output":"    violation\n"}` + "\n" +
			`{"Action":"fail","Test":"TestLayerA","Elapsed":1.0}` + "\n" +
			`{"Action":"run","Test":"TestLayerB"}` + "\n" +
			`{"Action":"pass","Test":"TestLayerB","Elapsed":0.1}` + "\n",
	)
	fakeExitErr := &exec.ExitError{}
	e := engine{
		exec:    fakeExec("TestLayerA\nTestLayerB\n", execOut, fakeExitErr),
		changed: changedRepoFiles,
	}

	report, err := e.run(context.Background(), Request{WorkspaceRoot: root})
	require.NoError(t, err, "test failure must not be an infra error")
	assert.False(t, report.Passed)

	byName := make(map[string]TestResult)
	for _, tr := range report.Tests {
		byName[tr.Name] = tr
	}
	assert.Equal(t, "fail", byName["TestLayerA"].Status)
	assert.Contains(t, byName["TestLayerA"].Output, "violation")
	assert.Equal(t, "pass", byName["TestLayerB"].Status)
}

// ---- Run via seam: infra error (non-ExitError) ----

func TestRun_InfraError(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
`,
	})

	infraErr := errors.New("context deadline exceeded")
	e := engine{
		exec:    fakeExec("TestFoo\n", nil, infraErr),
		changed: changedRepoFiles,
	}

	_, err := e.run(context.Background(), Request{WorkspaceRoot: root})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go test execution failed")
}

// ---- Run via seam: build-fail path ----
// Stub exec returns build-fail -json output + a *exec.ExitError but NO per-test
// events. Run must surface an infra error carrying the build diagnostic — an
// empty Report would render as a misleading "0 failing tests" (F1).

func TestRun_BuildFail(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
`,
	})

	// Build failure: go test emits build noise (no JSON test events), exit non-zero.
	buildFailOutput := []byte(
		"# tools/archtest\n" +
			"tools/archtest/broken_test.go:5:2: undefined: SomeMissing\n" +
			"FAIL\ttools/archtest [build failed]\n",
	)
	fakeExitErr := &exec.ExitError{}
	e := engine{
		exec:    fakeExec("TestFoo\n", buildFailOutput, fakeExitErr),
		changed: changedRepoFiles,
	}

	_, err := e.run(context.Background(), Request{WorkspaceRoot: root})
	require.Error(t, err, "build failure with zero test results must surface, not become an empty report")
	assert.Contains(t, err.Error(), "no test results")
	assert.Contains(t, err.Error(), "undefined: SomeMissing",
		"the build diagnostic must reach the operator instead of '0 failing tests'")
}

// TestRun_BuildFail_TestJSONOut_StillWritten verifies that the --test-json-out
// artifact is written BEFORE the build-failure error is returned, so CI's
// slowgate pipe still has its file even on a build break.
func TestRun_BuildFail_TestJSONOut_StillWritten(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
`,
	})

	buildFailOutput := []byte(
		`{"Action":"build-output","ImportPath":"tools/archtest","Output":"broken_test.go:5:2: undefined: SomeMissing\n"}` + "\n" +
			`{"Action":"build-fail","ImportPath":"tools/archtest"}` + "\n",
	)
	e := engine{
		exec:    fakeExec("TestFoo\n", buildFailOutput, &exec.ExitError{}),
		changed: changedRepoFiles,
	}

	jsonOutPath := filepath.Join(t.TempDir(), "events.json")
	req := Request{WorkspaceRoot: root, TestJSONOut: jsonOutPath}
	_, err := e.run(context.Background(), req)
	require.Error(t, err, "build failure must still error")

	_, statErr := os.Stat(jsonOutPath)
	assert.NoError(t, statErr, "TestJSONOut must be written before the build-failure error is returned")
}

// ---- Run via seam: TestJSONOut written ----

func TestRun_TestJSONOut_Written(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
`,
	})

	execOut := []byte(
		`go: build noise` + "\n" +
			`{"Action":"run","Test":"TestFoo"}` + "\n" +
			`{"Action":"pass","Test":"TestFoo","Elapsed":0.1}` + "\n",
	)
	e := engine{
		exec:    fakeExec("TestFoo\n", execOut, nil),
		changed: changedRepoFiles,
	}

	jsonOutPath := filepath.Join(t.TempDir(), "events.json")
	req := Request{WorkspaceRoot: root, TestJSONOut: jsonOutPath}
	report, err := e.run(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, report.Passed)

	data, readErr := os.ReadFile(jsonOutPath) //nolint:gosec // temp file from t.TempDir()
	require.NoError(t, readErr)
	content := string(data)
	// Only valid JSON event lines are written; build noise is filtered.
	assert.NotContains(t, content, "go: build noise")
	assert.Contains(t, content, `"Action":"run"`)
	assert.Contains(t, content, `"Action":"pass"`)
}

// ---- Run via seam: TestJSONOut written even when output is empty ----
// Regression: slowgate's `< file` must not fail because the file is missing.

func TestRun_TestJSONOut_WrittenEvenWhenEmpty(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
`,
	})

	// Stub returns empty output — simulates a subprocess that wrote nothing.
	e := engine{
		exec:    fakeExec("TestFoo\n", []byte{}, nil),
		changed: changedRepoFiles,
	}

	jsonOutPath := filepath.Join(t.TempDir(), "events.json")
	req := Request{WorkspaceRoot: root, TestJSONOut: jsonOutPath}
	_, err := e.run(context.Background(), req)
	require.NoError(t, err)

	// File must exist even though output was empty.
	_, statErr := os.Stat(jsonOutPath)
	assert.NoError(t, statErr, "TestJSONOut file must exist even when subprocess output is empty")
}

// ---- Run via seam: TestJSONOut file in nonexistent dir bubbles error ----

func TestRun_TestJSONOut_BadPath_ReturnsError(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFoo(t *testing.T) {}
`,
	})

	execOut := []byte(`{"Action":"pass","Test":"TestFoo","Elapsed":0.1}` + "\n")
	e := engine{
		exec:    fakeExec("TestFoo\n", execOut, nil),
		changed: changedRepoFiles,
	}

	// Path under a nonexistent directory — os.Create must fail.
	badPath := filepath.Join(t.TempDir(), "nonexistent-dir", "events.json")
	req := Request{WorkspaceRoot: root, TestJSONOut: badPath}
	_, err := e.run(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archtestrunner")
}

// ---- Run via seam: empty selection (--rule filter removes all) ----

func TestRun_EmptySelection_PassedTrue(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	// Build a fixture where rule exists but its test is NOT in the discovered set.
	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": `//go:build archtest

// INVARIANT: LAYER-05

package archtest

import "testing"

func TestLayerOnly(t *testing.T) {}
`,
	})

	// Discovery returns a test name that is NOT in the rule's test set.
	e := engine{
		exec: func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
			for _, a := range args {
				if a == "-list" {
					return []byte("TestOtherTest\n"), nil
				}
			}
			t.Error("execGoTest should not be called when selection is empty")
			return nil, nil
		},
		changed: changedRepoFiles,
	}

	req := Request{WorkspaceRoot: root, Rule: "LAYER-05"}
	report, err := e.run(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, report.Passed, "empty selection with no test failures must be Passed=true")
	assert.Empty(t, report.Selected)
}

// ---- Run via seam: TestJSONOut written on empty selection ----
// When no tests are selected, the file must still be created (empty).

func TestRun_EmptySelection_TestJSONOut_Created(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": `//go:build archtest

// INVARIANT: LAYER-05

package archtest

import "testing"

func TestLayerOnly(t *testing.T) {}
`,
	})

	e := engine{
		exec: func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
			for _, a := range args {
				if a == "-list" {
					return []byte("TestOtherTest\n"), nil
				}
			}
			t.Error("execGoTest should not be called when selection is empty")
			return nil, nil
		},
		changed: changedRepoFiles,
	}

	jsonOutPath := filepath.Join(t.TempDir(), "events.json")
	req := Request{WorkspaceRoot: root, Rule: "LAYER-05", TestJSONOut: jsonOutPath}
	report, err := e.run(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, report.Passed)

	_, statErr := os.Stat(jsonOutPath)
	assert.NoError(t, statErr, "TestJSONOut must be created even when selection is empty")
}

// ---- ListTests via seam ----

func TestListTests_ReturnsSelectedNames(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := t.TempDir()
	e := engine{
		exec: func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
			for _, a := range args {
				if a == "-list" {
					return []byte("TestAlpha\nTestBeta\nTestGamma\n"), nil
				}
			}
			t.Error("execGoTest must not be called from ListTests")
			return nil, nil
		},
		changed: changedRepoFiles,
	}

	names, err := e.listTests(context.Background(), Request{WorkspaceRoot: root})
	require.NoError(t, err)
	assert.Equal(t, []string{"TestAlpha", "TestBeta", "TestGamma"}, names)
}

func TestListTests_WithShard(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := t.TempDir()
	// The -list output is sorted by parseDiscoveryOutput, giving:
	// [TestAlpha, TestBeta, TestDelta, TestGamma] (alpha sort).
	// NR=1 (Alpha)→shard1, NR=2 (Beta)→shard0, NR=3 (Delta)→shard1, NR=4 (Gamma)→shard0.
	// shard 0: [TestBeta, TestGamma].
	e := engine{
		exec:    fakeDiscoverExec("TestAlpha", "TestBeta", "TestGamma", "TestDelta"),
		changed: changedRepoFiles,
	}

	names, err := e.listTests(context.Background(), Request{
		WorkspaceRoot: root,
		Shard:         Shard{Index: 0, Total: 2},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"TestBeta", "TestGamma"}, names)
}

func TestListTests_InvalidShard_Error(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}
	e := newEngine()
	_, err := e.listTests(context.Background(), Request{
		WorkspaceRoot: t.TempDir(),
		Shard:         Shard{Index: 5, Total: 3}, // index out of range
	})
	require.Error(t, err)
}

// ---- resolveSelected via seam: changed-files path using changedFilesFn ----

// TestApplyFilters_Changed_EmptySet verifies that Changed=true with empty
// changed files returns empty selection and Run returns Passed=true without
// spawning a subprocess.
func TestApplyFilters_Changed_EmptySet(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"alpha_test.go": `//go:build archtest

// INVARIANT: ALPHA-01

package archtest

import "testing"

func TestAlphaX(t *testing.T) {}
`,
	})

	execCalled := false
	e := engine{
		exec: func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
			for _, a := range args {
				if a == "-list" {
					return []byte("TestAlphaX\n"), nil
				}
			}
			execCalled = true
			return nil, nil
		},
		// Changed returns no archtest files.
		changed: func(_ context.Context, _ string) ([]string, error) {
			return nil, nil
		},
	}

	req := Request{WorkspaceRoot: root, Changed: true}
	report, err := e.run(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, report.Passed)
	assert.Empty(t, report.Selected)
	assert.False(t, execCalled, "exec subprocess must not be called when changed set is empty")
}

// TestApplyFilters_Changed_Subset verifies the full run path under source-aware
// --changed: a change confined to one rule's scan domain selects that rule and
// excludes a rule scoped to an unrelated area.
func TestApplyFilters_Changed_Subset(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := makeFakeArchtestDir(t, map[string]string{
		"alpha_test.go": `//go:build archtest

// INVARIANT: ALPHA-01

package archtest

import "testing"

func TestAlphaX(t *testing.T) { Run(t, Typed(TypedOpts{}, []string{"./adapters/redis/..."}), nil) }
`,
		"beta_test.go": `//go:build archtest

// INVARIANT: BETA-01

package archtest

import "testing"

func TestBetaX(t *testing.T) { Run(t, Typed(TypedOpts{}, []string{"./framework/kernel/..."}), nil) }
`,
	})

	execOut := []byte(
		`{"Action":"run","Test":"TestAlphaX"}` + "\n" +
			`{"Action":"pass","Test":"TestAlphaX","Elapsed":0.05}` + "\n",
	)
	e := engine{
		exec: fakeExec("TestAlphaX\nTestBetaX\n", execOut, nil),
		// A source change under adapters/redis affects only the redis-scoped rule.
		changed: func(_ context.Context, _ string) ([]string, error) {
			return []string{"adapters/redis/client.go"}, nil
		},
	}

	req := Request{WorkspaceRoot: root, Changed: true}
	report, err := e.run(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, report.Passed)
	assert.Equal(t, []string{"TestAlphaX"}, report.Selected,
		"only rules whose scan domain contains the changed source should be selected")
}

// ---- resolveSelected via seam: rule-based selection ----

func TestResolveSelected_ChangedPath_IntersectsArchtestFiles(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	// Build a fixture workspace with two archtest files.
	root := makeFakeArchtestDir(t, map[string]string{
		"alpha_test.go": `//go:build archtest

// INVARIANT: ALPHA-01

package archtest

import "testing"

func TestAlphaX(t *testing.T) {}
`,
		"beta_test.go": `//go:build archtest

// INVARIANT: BETA-01

package archtest

import "testing"

func TestBetaX(t *testing.T) {}
`,
	})

	e := engine{
		exec:    fakeDiscoverExec("TestAlphaX", "TestBetaX"),
		changed: changedRepoFiles,
	}

	// Verify rule-based selection.
	req := Request{WorkspaceRoot: root, Rule: "ALPHA-01"}
	names, err := e.listTests(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"TestAlphaX"}, names)
}
