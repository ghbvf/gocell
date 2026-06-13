//go:build archtest

// INVARIANT: ARCHTEST-CI-GOWORK-ACTIVE-01
package archtest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// verifyArchtestScriptMarker identifies a CI run line invoking the full-sweep
// wrapper script (hack/verify-archtest.sh), which `exec`s `go run ./cmd/gocell
// verify archtest "$@"` — i.e. the SAME whole-workspace production scan as the
// CLI marker. Deliberately NOT a substring of hack/verify-archtest-invariants.sh
// (the PR-time CHEAP subset), so the invariants script — which runs only the
// `-run` allowlist of non-workspace-loading tests (go.mod / go.work parse,
// header scans, one tiny ModeModule fixture) and is therefore GOWORK-agnostic —
// is not (over-)constrained by this guard.
const verifyArchtestScriptMarker = "verify-archtest.sh"

// goworkOffInlineRe matches a shell GOWORK=off assignment on a command line —
// either a command-prefix (`GOWORK=off gocell verify archtest`) or an
// `export GOWORK=off`. Quotes optional; value compared case-sensitively to the
// literal `off` (Go's os.Getenv("GOWORK")=="off" is exact, mirroring the runtime
// checkGOWORK guard in cmd/gocell/internal/archtestrunner/runner.go).
var goworkOffInlineRe = regexp.MustCompile(`GOWORK=["']?off["']?`)

// goworkWorkflow parses only the env scopes + step run blocks needed for the
// GOWORK-active assertion. Decoupled from archtest_ci_shard_count_test.go's
// archtestWorkflowConfig (different concern: env, not the shard matrix) and from
// ci_pinning_test.go's types, so field growth here cannot regress those guards.
//
// env values are yaml.Node (not string): GitHub Actions treats env values as
// strings, but go-yaml resolves the bareword `off` to a !!bool under the YAML
// core schema — so `GOWORK: off` would never land in a map[string]string. Reading
// yaml.Node.Value recovers the literal source token ("off") regardless of the
// resolved tag, which is exactly the byte a runner would export.
type goworkWorkflow struct {
	Env  map[string]yaml.Node `yaml:"env"`
	Jobs map[string]goworkJob `yaml:"jobs"`
}

type goworkJob struct {
	Env   map[string]yaml.Node `yaml:"env"`
	Steps []goworkStep         `yaml:"steps"`
}

type goworkStep struct {
	Name string               `yaml:"name"`
	Env  map[string]yaml.Node `yaml:"env"`
	Run  string               `yaml:"run"`
}

// TestArchtestCIGoworkActive enforces ARCHTEST-CI-GOWORK-ACTIVE-01 across every
// .github/workflows/*.y{a,}ml: no CI step that runs a whole-workspace archtest
// production scan may have GOWORK=off in effect (workflow/job/step env, or an
// inline shell assignment in the same run block).
//
// Why it matters: archtest's production scan loads packages via go/packages in
// ModeWorkspace (one ./<dir>/... pattern per go.work member). With GOWORK=off the
// go command drops to single-module mode and silently loads only the root module
// — every satellite (adapters/*, corecells, examples/*, …) vanishes from the scan
// and every workspace-spanning rule false-greens. (Canonical proof of the
// silent-drop class: github/codeql PR #20675; GOWORK=off single-module semantics:
// golang/go proposal 45713-workspace.md.) The runtime checkGOWORK() fail-fast in
// the CLI runner already rejects GOWORK=off on the `gocell verify archtest` path
// (the SOLE whole-workspace scan entrypoint — nightly + local make verify); this
// static guard is the defense-in-depth layer that rejects the misconfiguration in
// the CI definition itself, before a runner ever starts.
//
// Markers (whole-workspace scan entrypoints only):
//   - verifyArchtestCLIMarker  "verify archtest"     — the CLI subcommand.
//   - verifyArchtestScriptMarker "verify-archtest.sh" — the full-sweep wrapper.
//
// Deliberately NOT markers: bare `go test ./tools/archtest` (the only CI use is
// test-race.yml's internal -race unit test of ./tools/archtest/internal/..., NOT
// a production scan; matching it would false-fire), and hack/verify-archtest-
// invariants.sh (the PR-time cheap subset is GOWORK-agnostic; see its godoc).
//
// AI-robust grade: Medium content-scan (machine-checkable; a GOWORK=off added to
// an archtest step is a reviewer-visible diff that fails this test). Hard is
// unreachable — an env-var runtime precondition cannot be made a compile error
// (same terminal ceiling as ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01). Paired with the
// runtime checkGOWORK() for the CLI path.
//
// Blind spot (disclosed): a future step that runs a whole-workspace production
// scan via a NEW entrypoint matching neither marker AND bypasses the CLI runner
// (e.g. raw `go test -tags=archtest ./tools/archtest -run TestSomeProdScan` with
// GOWORK=off) evades both this YAML scan and checkGOWORK. The established routing
// — all whole-workspace scans go through the CLI; the invariant subset excludes
// them — makes that an active violation, not an accident.
//
// ref: github/codeql pull/20675 (go workspace cross-module coverage gap)
// ref: golang/go proposal 45713-workspace.md (GOWORK=off single-module semantics)
// ref: kubernetes/kubernetes hack/verify-import-boss.sh (go work edit -json: scan
//
//	the whole workspace, never a single module)
//
// ADR: docs/architecture/202606041600-1555-adr-archtest-workspace-root-model.md
// §Operational notes (#1590 close-out).
func TestArchtestCIGoworkActive(t *testing.T) {
	root := findModuleRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "ARCHTEST-CI-GOWORK-ACTIVE-01: read .github/workflows")

	total := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		body, rerr := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		require.NoError(t, rerr)
		matched, verr := validateArchtestGoworkActive(name, body)
		require.NoError(t, verr)
		total += matched
	}

	// Anti-vacuity: the marker MUST match at least one real archtest step across
	// the workflow set, or the guard would silently pass on everything (marker
	// drift / the nightly scan removed).
	require.Positivef(t, total,
		"ARCHTEST-CI-GOWORK-ACTIVE-01: no archtest scan invocation (%q / %q) found in any "+
			".github/workflows/*.yml — marker drift or the whole-workspace scan was removed; "+
			"the guard is vacuous. archtest-nightly.yml must run `gocell verify archtest`.",
		verifyArchtestCLIMarker, verifyArchtestScriptMarker)
}

// validateArchtestGoworkActive checks one workflow file. It returns the number of
// archtest-scan steps found (for the caller's anti-vacuity tally) and the first
// violation, if any. A non-archtest workflow returns (0, nil) — not an error.
func validateArchtestGoworkActive(name string, body []byte) (int, error) {
	var wf goworkWorkflow
	if err := yaml.NewDecoder(bytes.NewReader(body)).Decode(&wf); err != nil {
		return 0, fmt.Errorf("ARCHTEST-CI-GOWORK-ACTIVE-01: parse %s: %w", name, err)
	}
	workflowOff := goworkEnvOff(wf.Env)
	matched := 0
	for _, jobName := range goworkJobNames(wf.Jobs) {
		job := wf.Jobs[jobName]
		jobOff := goworkEnvOff(job.Env)
		for _, step := range job.Steps {
			cmds := runCommandLines(step.Run)
			if !stepRunsArchtestScan(cmds) {
				continue
			}
			matched++
			scope, off := goworkOffScope(workflowOff, jobOff, goworkEnvOff(step.Env), stepInlineGoworkOff(cmds))
			if off {
				return matched, fmt.Errorf("ARCHTEST-CI-GOWORK-ACTIVE-01: %s job %q step %q runs an "+
					"archtest whole-workspace scan with GOWORK=off in effect (%s); the production scan "+
					"requires ModeWorkspace or satellite modules silently drop from the scan. Remove the "+
					"GOWORK=off (unset it / point it at the repo go.work). See ADR 202606041600-1555 "+
					"§Operational notes (#1590).", name, jobName, stepLabel(step), scope)
			}
		}
	}
	return matched, nil
}

// stepRunsArchtestScan reports whether any logical command line invokes a
// whole-workspace archtest production scan (CLI subcommand or full-sweep script).
// cmds are comment-stripped, continuation-joined lines (runCommandLines), so a
// commented example cannot make a step count as an archtest step.
func stepRunsArchtestScan(cmds []string) bool {
	for _, cmd := range cmds {
		if strings.Contains(cmd, verifyArchtestCLIMarker) || strings.Contains(cmd, verifyArchtestScriptMarker) {
			return true
		}
	}
	return false
}

// stepInlineGoworkOff reports whether any logical command line in the step sets
// GOWORK=off (command-prefix or `export`). Comment-stripped lines only, so a
// `# GOWORK=off` doc note does not trip the guard.
func stepInlineGoworkOff(cmds []string) bool {
	for _, cmd := range cmds {
		if goworkOffInlineRe.MatchString(cmd) {
			return true
		}
	}
	return false
}

// goworkEnvOff reports whether an env map declares GOWORK=off. Reads the raw
// yaml.Node.Value so the YAML-bool resolution of the bareword `off` does not hide
// it (see goworkWorkflow godoc).
func goworkEnvOff(env map[string]yaml.Node) bool {
	n, ok := env["GOWORK"]
	return ok && strings.EqualFold(strings.TrimSpace(n.Value), "off")
}

// goworkOffScope reduces the four scopes to (label, isOff) for the error message,
// reported in precedence order workflow → job → step → inline.
func goworkOffScope(workflowOff, jobOff, stepOff, inlineOff bool) (string, bool) {
	switch {
	case workflowOff:
		return "workflow-level env.GOWORK=off", true
	case jobOff:
		return "job-level env.GOWORK=off", true
	case stepOff:
		return "step-level env.GOWORK=off", true
	case inlineOff:
		return "inline shell GOWORK=off in the run block", true
	default:
		return "", false
	}
}

func stepLabel(s goworkStep) string {
	if s.Name != "" {
		return s.Name
	}
	return "(unnamed)"
}

// goworkJobNames returns job ids in deterministic order so the first reported
// violation is stable across runs.
func goworkJobNames(m map[string]goworkJob) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- synthetic fixtures: red (must fail) ---------------------------------

// goworkRedCases are minimal workflows that DO run an archtest scan but disable
// the workspace. Each must fail validateArchtestGoworkActive.
func TestArchtestCIGoworkActive_RejectsGoworkOff(t *testing.T) {
	cases := map[string]string{
		"step-env": `jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        env:
          GOWORK: off
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24
`,
		"job-env": `jobs:
  verify-archtest:
    env:
      GOWORK: off
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24
`,
		"workflow-env": `env:
  GOWORK: off
jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24
`,
		"inline-prefix": `jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        run: |
          GOWORK=off "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24
`,
		"inline-export": `jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        run: |
          export GOWORK=off
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24
`,
		"inline-quoted": `jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        run: |
          GOWORK="off" "$RUNNER_TEMP/gocell" verify archtest
`,
		"script-wrapper-off": `jobs:
  full-sweep:
    env:
      GOWORK: off
    steps:
      - name: Full archtest sweep
        run: bash hack/verify-archtest.sh
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			matched, err := validateArchtestGoworkActive("fixture.yml", []byte(body))
			require.Error(t, err, "GOWORK=off (%s) must be rejected", name)
			require.Positive(t, matched, "fixture must be recognized as an archtest step")
		})
	}
}

// --- synthetic fixtures: green (must pass + be non-vacuous) ---------------

func TestArchtestCIGoworkActive_AcceptsGoworkActive(t *testing.T) {
	cases := map[string]string{
		"no-gowork": `jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24
`,
		"gowork-path": `env:
  GOWORK: ${{ github.workspace }}/go.work
jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24
`,
		// anti-vacuity: a commented GOWORK=off above a real scan must NOT trip the
		// guard (comment-stripping in runCommandLines), proving the inline check
		// binds to actual command text — the F3-class false-positive guard.
		"commented-off": `jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        run: |
          # never do this: GOWORK=off drops satellites
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24
`,
		"script-wrapper-clean": `jobs:
  full-sweep:
    steps:
      - name: Full archtest sweep
        run: bash hack/verify-archtest.sh
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			matched, err := validateArchtestGoworkActive("fixture.yml", []byte(body))
			require.NoError(t, err)
			require.Positive(t, matched, "fixture must be recognized as an archtest step (non-vacuous)")
		})
	}
}

// A workflow that runs NO archtest scan must return (0, nil): the guard is
// per-file scoped, and the marker must not over-match (e.g. the internal -race
// unit test of ./tools/archtest/internal/... is not a production scan).
func TestArchtestCIGoworkActive_IgnoresNonArchtestWorkflow(t *testing.T) {
	body := `jobs:
  race-unit:
    env:
      GOWORK: off
    steps:
      - name: Race unit tests
        run: go test -race ./tools/archtest/internal/typeseval/...
`
	matched, err := validateArchtestGoworkActive("test-race.yml", []byte(body))
	require.NoError(t, err, "a non-archtest-scan step setting GOWORK=off is out of scope")
	require.Zero(t, matched, "bare `go test ./tools/archtest/internal/...` must not match the scan marker")
}
