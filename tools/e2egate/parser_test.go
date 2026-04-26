package e2egate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/e2egate"
)

// Test fixtures: each block represents the output of `go test -json`. Lines
// are JSON-encoded test2json events. We craft minimal valid streams to drive
// each gate decision branch.

const eventsAllPass = `{"Action":"run","Package":"pkg/a","Test":"TestOne"}
{"Action":"output","Package":"pkg/a","Test":"TestOne","Output":"=== RUN   TestOne\n"}
{"Action":"pass","Package":"pkg/a","Test":"TestOne","Elapsed":0.1}
{"Action":"run","Package":"pkg/a","Test":"TestTwo"}
{"Action":"pass","Package":"pkg/a","Test":"TestTwo","Elapsed":0.1}
{"Action":"run","Package":"pkg/a","Test":"TestThree"}
{"Action":"pass","Package":"pkg/a","Test":"TestThree","Elapsed":0.1}
{"Action":"pass","Package":"pkg/a","Elapsed":0.4}
`

const eventsMixedPassSkip = `{"Action":"run","Package":"pkg/a","Test":"TestOne"}
{"Action":"pass","Package":"pkg/a","Test":"TestOne","Elapsed":0.1}
{"Action":"run","Package":"pkg/a","Test":"TestTwo"}
{"Action":"pass","Package":"pkg/a","Test":"TestTwo","Elapsed":0.1}
{"Action":"run","Package":"pkg/a","Test":"TestThree"}
{"Action":"skip","Package":"pkg/a","Test":"TestThree","Elapsed":0}
{"Action":"pass","Package":"pkg/a","Elapsed":0.3}
`

const eventsAllSkipConditional = `{"Action":"run","Package":"pkg/e2e","Test":"TestE2E_A"}
{"Action":"output","Package":"pkg/e2e","Test":"TestE2E_A","Output":"--- SKIP: TestE2E_A\n"}
{"Action":"skip","Package":"pkg/e2e","Test":"TestE2E_A","Elapsed":0}
{"Action":"run","Package":"pkg/e2e","Test":"TestE2E_B"}
{"Action":"skip","Package":"pkg/e2e","Test":"TestE2E_B","Elapsed":0}
{"Action":"run","Package":"pkg/e2e","Test":"TestE2E_C"}
{"Action":"skip","Package":"pkg/e2e","Test":"TestE2E_C","Elapsed":0}
{"Action":"pass","Package":"pkg/e2e","Elapsed":0.0}
`

const eventsPackageLevelSkip = `{"Action":"skip","Package":"pkg/notests","Output":"?   pkg/notests   [no test files]\n"}
`

const eventsBuildFail = `{"Action":"output","Package":"pkg/broken","Output":"FAIL    pkg/broken [build failed]\n"}
{"Action":"fail","Package":"pkg/broken","Elapsed":0}
`

const eventsEmpty = ``

const eventsInvalidJSON = `{"Action":"run","Package":"pkg/a","Test":"TestOne"}
not-a-valid-json-line
{"Action":"pass","Package":"pkg/a","Test":"TestOne","Elapsed":0.1}
`

const eventsMultiPackageMixed = `{"Action":"run","Package":"pkg/a","Test":"TestA"}
{"Action":"pass","Package":"pkg/a","Test":"TestA","Elapsed":0.1}
{"Action":"pass","Package":"pkg/a","Elapsed":0.1}
{"Action":"run","Package":"pkg/b","Test":"TestB"}
{"Action":"fail","Package":"pkg/b","Test":"TestB","Elapsed":0.2}
{"Action":"fail","Package":"pkg/b","Elapsed":0.2}
`

// eventsHelperPackagePlusRealTests mirrors `go test ./tests/e2e/...` when the
// expansion includes both a real test package and a helper package without any
// _test.go files (e.g., tests/e2e/internal/require). The helper emits a
// package-level skip with no test events; the real package executes tests.
// The gate must accept this — only an *all-packages-no-execution* run fails.
const eventsHelperPackagePlusRealTests = `{"Action":"skip","Package":"pkg/helper","Output":"?   pkg/helper   [no test files]\n"}
{"Action":"run","Package":"pkg/real","Test":"TestReal"}
{"Action":"pass","Package":"pkg/real","Test":"TestReal","Elapsed":0.1}
{"Action":"pass","Package":"pkg/real","Elapsed":0.1}
`

// eventsSubTests has one parent test with three t.Run children; counting both
// parent and children would inflate TotalExecuted to 4. The parser must count
// only the parent (top-level) terminal event.
const eventsSubTests = `{"Action":"run","Package":"pkg/sub","Test":"TestParent"}
{"Action":"run","Package":"pkg/sub","Test":"TestParent/SubA"}
{"Action":"pass","Package":"pkg/sub","Test":"TestParent/SubA","Elapsed":0.0}
{"Action":"run","Package":"pkg/sub","Test":"TestParent/SubB"}
{"Action":"pass","Package":"pkg/sub","Test":"TestParent/SubB","Elapsed":0.0}
{"Action":"run","Package":"pkg/sub","Test":"TestParent/SubC"}
{"Action":"pass","Package":"pkg/sub","Test":"TestParent/SubC","Elapsed":0.0}
{"Action":"pass","Package":"pkg/sub","Test":"TestParent","Elapsed":0.1}
{"Action":"pass","Package":"pkg/sub","Elapsed":0.1}
`

func TestParse_AllPass_GatePasses(t *testing.T) {
	res, err := e2egate.Parse(strings.NewReader(eventsAllPass))
	require.NoError(t, err)
	assert.False(t, res.Failed(), "gate should pass on all-pass run; reasons=%v", res.Reasons)
	assert.Equal(t, 3, res.TotalExecuted)
	assert.Equal(t, 0, res.TotalSkipped)
	require.Contains(t, res.Packages, "pkg/a")
	assert.Equal(t, 3, res.Packages["pkg/a"].Executed)
	assert.Equal(t, 0, res.Packages["pkg/a"].Skipped)
	assert.Equal(t, "pass", res.Packages["pkg/a"].Action)
}

func TestParse_MixedPassSkip_GatePasses(t *testing.T) {
	res, err := e2egate.Parse(strings.NewReader(eventsMixedPassSkip))
	require.NoError(t, err)
	assert.False(t, res.Failed(), "gate should pass when at least one test executed; reasons=%v", res.Reasons)
	assert.Equal(t, 2, res.TotalExecuted)
	assert.Equal(t, 1, res.TotalSkipped)
}

func TestParse_AllSkipConditional_GateFails(t *testing.T) {
	res, err := e2egate.Parse(strings.NewReader(eventsAllSkipConditional))
	require.NoError(t, err)
	assert.True(t, res.Failed(), "gate should fail when every test in the package was skipped")
	assert.Equal(t, 0, res.TotalExecuted)
	assert.Equal(t, 3, res.TotalSkipped)
	require.NotEmpty(t, res.Reasons)
	joined := strings.Join(res.Reasons, "\n")
	assert.Contains(t, joined, "pkg/e2e", "reason should name the offending package")
	assert.Contains(t, joined, "all-skipped", "reason should label the failure mode")
}

func TestParse_PackageLevelSkip_NoTestFiles_GateFails(t *testing.T) {
	res, err := e2egate.Parse(strings.NewReader(eventsPackageLevelSkip))
	require.NoError(t, err)
	assert.True(t, res.Failed(), "gate must fail when the only event is a package-level skip with zero executed tests")
	assert.Equal(t, 0, res.TotalExecuted)
}

func TestParse_BuildFail_GateFails(t *testing.T) {
	res, err := e2egate.Parse(strings.NewReader(eventsBuildFail))
	require.NoError(t, err)
	assert.True(t, res.Failed(), "gate should fail on build failure")
	require.Contains(t, res.Packages, "pkg/broken")
	assert.True(t, res.Packages["pkg/broken"].BuildFailed)
	joined := strings.Join(res.Reasons, "\n")
	assert.Contains(t, joined, "pkg/broken")
	assert.Contains(t, joined, "build")
}

func TestParse_EmptyStdin_GateFails(t *testing.T) {
	res, err := e2egate.Parse(strings.NewReader(eventsEmpty))
	require.NoError(t, err)
	assert.True(t, res.Failed(), "gate must fail on empty input")
	assert.Equal(t, 0, res.TotalExecuted)
	assert.NotEmpty(t, res.Reasons)
}

func TestParse_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := e2egate.Parse(strings.NewReader(eventsInvalidJSON))
	require.Error(t, err, "Parse must surface JSON decoder errors so the caller exits non-zero")
}

func TestParse_HelperPackageWithoutTests_GatePasses(t *testing.T) {
	// Real test package executed; helper had no _test.go files. The gate
	// must NOT fail just because the helper emitted a package-level skip.
	res, err := e2egate.Parse(strings.NewReader(eventsHelperPackagePlusRealTests))
	require.NoError(t, err)
	assert.False(t, res.Failed(),
		"helper packages without test files must not fail the gate; reasons=%v", res.Reasons)
	assert.Equal(t, 1, res.TotalExecuted)
	require.Contains(t, res.Packages, "pkg/helper")
	require.Contains(t, res.Packages, "pkg/real")
	assert.Equal(t, "skip", res.Packages["pkg/helper"].Action)
	assert.Equal(t, 0, res.Packages["pkg/helper"].Executed)
	assert.Equal(t, 0, res.Packages["pkg/helper"].Skipped)
}

func TestParse_SubTests_CountedOncePerParent(t *testing.T) {
	res, err := e2egate.Parse(strings.NewReader(eventsSubTests))
	require.NoError(t, err)
	assert.False(t, res.Failed(), "gate should pass when parent test ran; reasons=%v", res.Reasons)
	// One parent test (3 subtests are not counted separately).
	assert.Equal(t, 1, res.TotalExecuted)
	require.Contains(t, res.Packages, "pkg/sub")
	assert.Equal(t, 1, res.Packages["pkg/sub"].Executed)
	assert.Equal(t, 0, res.Packages["pkg/sub"].Skipped)
}

func TestParse_MultiPackageOnePassOneFail_GatePasses(t *testing.T) {
	// Test failures are not gate failures; the gate only checks that at
	// least one test executed and no package was wholly skipped.
	res, err := e2egate.Parse(strings.NewReader(eventsMultiPackageMixed))
	require.NoError(t, err)
	assert.False(t, res.Failed(), "gate should pass — a failing test still counts as executed; reasons=%v", res.Reasons)
	assert.Equal(t, 2, res.TotalExecuted)
	require.Contains(t, res.Packages, "pkg/a")
	require.Contains(t, res.Packages, "pkg/b")
	assert.Equal(t, "pass", res.Packages["pkg/a"].Action)
	assert.Equal(t, "fail", res.Packages["pkg/b"].Action)
}
