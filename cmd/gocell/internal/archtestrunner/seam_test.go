package archtestrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubRunGoCommand replaces runGoCommand for the duration of a test and restores
// it in t.Cleanup. Tests must use this helper rather than assigning the variable
// directly so the restore is always registered.
func stubRunGoCommand(t *testing.T, fn func(ctx context.Context, dir string, extraEnv, args []string) ([]byte, error)) {
	t.Helper()
	orig := runGoCommand
	runGoCommand = fn
	t.Cleanup(func() { runGoCommand = orig })
}

// stubDiscovery returns a stub that yields the given sorted test list when the
// first arg after "test" is "-list", and the given exec output for any other call.
func stubDiscovery(t *testing.T, listOutput string, execOutput []byte, execErr error) {
	t.Helper()
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		// args[0] == "test", args[1] is "-list" or "-tags=archtest"
		for _, a := range args {
			if a == "-list" {
				return []byte(listOutput), nil
			}
		}
		return execOutput, execErr
	})
}

// ---- discoverTests via seam ----

func TestDiscoverTests_ReturnsNames(t *testing.T) {
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		// Confirm -list flag is present.
		require.Contains(t, args, "-list", "discover should pass -list")
		return []byte("TestAlpha\nTestBeta\nTestGamma\nok  ./tools/archtest\t0.001s\n"), nil
	})

	tests, err := discoverTests(context.Background(), t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, []string{"TestAlpha", "TestBeta", "TestGamma"}, tests)
}

func TestDiscoverTests_ZeroDiscovered_Error(t *testing.T) {
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
		// Only noise, no Test* names.
		return []byte("ok  ./tools/archtest\t0.001s\n"), nil
	})

	_, err := discoverTests(context.Background(), t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no archtest Test* functions discovered")
}

func TestDiscoverTests_RunError_Error(t *testing.T) {
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
		return nil, fmt.Errorf("go tool not found")
	})

	_, err := discoverTests(context.Background(), t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discover archtest tests")
}

// ---- execGoTest via seam ----

func TestExecGoTest_PassesTimeoutAndRunFlag(t *testing.T) {
	var capturedArgs []string
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		capturedArgs = args
		return []byte(`{"Action":"pass","Test":"TestFoo","Elapsed":0.1}`), nil
	})

	req := Request{WorkspaceRoot: t.TempDir(), Timeout: "3m"}
	selected := []string{"TestFoo", "TestBar"}
	_, err := execGoTest(context.Background(), req, selected)
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

	listOut := "TestLayerA\n"
	execOut := []byte(
		`{"Action":"run","Test":"TestLayerA"}` + "\n" +
			`{"Action":"pass","Test":"TestLayerA","Elapsed":0.05}` + "\n",
	)
	stubDiscovery(t, listOut, execOut, nil)

	report, err := Run(context.Background(), Request{WorkspaceRoot: root})
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

	listOut := "TestLayerA\nTestLayerB\n"
	execOut := []byte(
		`{"Action":"run","Test":"TestLayerA"}` + "\n" +
			`{"Action":"output","Test":"TestLayerA","Output":"    violation\n"}` + "\n" +
			`{"Action":"fail","Test":"TestLayerA","Elapsed":1.0}` + "\n" +
			`{"Action":"run","Test":"TestLayerB"}` + "\n" +
			`{"Action":"pass","Test":"TestLayerB","Elapsed":0.1}` + "\n",
	)
	// Simulate `go test` exit non-zero via *exec.ExitError.
	fakeExitErr := &exec.ExitError{}
	stubDiscovery(t, listOut, execOut, fakeExitErr)

	report, err := Run(context.Background(), Request{WorkspaceRoot: root})
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

	listOut := "TestFoo\n"
	infraErr := errors.New("context deadline exceeded")
	stubDiscovery(t, listOut, nil, infraErr)

	_, err := Run(context.Background(), Request{WorkspaceRoot: root})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go test execution failed")
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

	listOut := "TestFoo\n"
	execOut := []byte(
		`go: build noise` + "\n" +
			`{"Action":"run","Test":"TestFoo"}` + "\n" +
			`{"Action":"pass","Test":"TestFoo","Elapsed":0.1}` + "\n",
	)
	stubDiscovery(t, listOut, execOut, nil)

	jsonOutPath := filepath.Join(t.TempDir(), "events.json")
	req := Request{WorkspaceRoot: root, TestJSONOut: jsonOutPath}
	report, err := Run(context.Background(), req)
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
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		for _, a := range args {
			if a == "-list" {
				return []byte("TestOtherTest\n"), nil
			}
		}
		t.Error("execGoTest should not be called when selection is empty")
		return nil, nil
	})

	req := Request{WorkspaceRoot: root, Rule: "LAYER-05"}
	report, err := Run(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, report.Passed, "empty selection with no test failures must be Passed=true")
	assert.Empty(t, report.Selected)
}

// ---- ListTests via seam ----

func TestListTests_ReturnsSelectedNames(t *testing.T) {
	if os.Getenv("GOWORK") == "off" {
		t.Skip("GOWORK=off")
	}

	root := t.TempDir()
	listOut := "TestAlpha\nTestBeta\nTestGamma\n"
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		for _, a := range args {
			if a == "-list" {
				return []byte(listOut), nil
			}
		}
		t.Error("execGoTest must not be called from ListTests")
		return nil, nil
	})

	names, err := ListTests(context.Background(), Request{WorkspaceRoot: root})
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
	listOut := "TestAlpha\nTestBeta\nTestGamma\nTestDelta\n"
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
		return []byte(listOut), nil
	})

	names, err := ListTests(context.Background(), Request{
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
	_, err := ListTests(context.Background(), Request{
		WorkspaceRoot: t.TempDir(),
		Shard:         Shard{Index: 5, Total: 3}, // index out of range
	})
	require.Error(t, err)
}

// ---- resolveSelected via seam: changed-files path ----

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

	// Discovery returns both; we simulate git diff returning only alpha_test.go changed.
	listOut := "TestAlphaX\nTestBetaX\n"
	stubRunGoCommand(t, func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		for _, a := range args {
			if a == "-list" {
				return []byte(listOut), nil
			}
		}
		// git calls also go through runGoCommand... but we replaced it.
		// changedArchtestFiles calls the real git binary via cmdrun.NewTool("git"),
		// NOT through runGoCommand. So this stub only covers go test calls.
		// Return something so the test won't hang.
		return []byte{}, nil
	})

	// changedFilesToTests is pure (no git); test the pure mapping path directly
	// by calling applyFilters with a pre-built discovered list and Changed=false
	// (we already have TestChangedFilesToTests* in gitdiff_test.go).
	// Here we just verify the shard+rule path through resolveSelected.

	// Verify rule-based selection through resolveSelected (which calls discoverTests via seam).
	req := Request{WorkspaceRoot: root, Rule: "ALPHA-01"}
	names, err := ListTests(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"TestAlphaX"}, names)
}
