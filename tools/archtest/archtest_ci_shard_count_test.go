// INVARIANT: ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01
package archtest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// verifyArchtestScriptMarker is the substring that identifies a step actually
// invoking the verify-archtest script. GitHub Actions step env (`steps[*].env`)
// only applies during that specific step's process. The validator MUST bind
// the SHARD_COUNT=16 assertion to the SAME step that runs this script — a
// dummy/setup step with SHARD_COUNT=16 in env cannot satisfy the contract
// because its env never reaches the actual `bash hack/verify-archtest.sh`
// execution. ref: GitHub Docs jobs.<job_id>.steps[*].env.
const verifyArchtestScriptMarker = "hack/verify-archtest.sh"

// TestVerifyArchtestCIExplicitShardCount asserts that the verify-archtest
// CI job in .github/workflows/archtest-nightly.yml sets SHARD_COUNT=16
// explicitly in step env, rather than relying on the script default.
//
// Background: hack/verify-archtest.sh default SHARD_COUNT is 1 (local-friendly:
// single process, ~300 Test* share *types.Info cache, lowest CPU). CI must set
// SHARD_COUNT=16 explicitly — GHA 2-core 7 GB shard runner cannot survive a
// single-process run that accumulates 20+ GB peak RSS (PR #445 OOM SIGTERM).
// If CI yaml drops the explicit value, the script falls through to K=1 and
// the next CI run blows up.
//
// AI-rebust: Medium runtime guard. The constraint "CI yaml verify-archtest
// must explicit SHARD_COUNT=16" cannot be bypassed without modifying this
// archtest in the same PR. Violation is reviewer-visible diff.
//
// History: PR #878 introduced this guard against .github/workflows/_build-
// lint.yml (the then-only CI gate). Per ADR 202605120000 §Amendment 2026-
// 05-23-pr-time-to-nightly the matrix moved to archtest-nightly.yml and the
// PR-time job was deleted; the guard now targets the nightly yaml. Local
// PR-time fast-feedback is hack/githooks/pre-push (K=1 sweep).
//
// ref: ADR docs/architecture/202605120000-adr-archtest-process-isolation.md
// §Amendment 2026-05-23 + §Amendment 2026-05-23-pr-time-to-nightly.
func TestVerifyArchtestCIExplicitShardCount(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "workflows", "archtest-nightly.yml")))
	require.NoError(t, err)
	require.NoError(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsMissingEnv is the negative
// fixture: a verify-archtest job whose step omits SHARD_COUNT must fail
// validation. Guards against future yaml refactor silently dropping the env.
func TestVerifyArchtestCIExplicitShardCountRejectsMissingEnv(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard ${{ matrix.shard }}
        env:
          SHARD_TARGET: ${{ matrix.shard }}
          TIMEOUT: 5m
        run: bash hack/verify-archtest.sh
`)
	require.Error(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsWrongValue covers the value
// drift case: env present but value != 16 (e.g. mistakenly set to 1, 8, or
// any non-CI value). Validator must reject any value other than 16 since
// K=16 is the only K that fits under the GHA 7 GB shard budget.
func TestVerifyArchtestCIExplicitShardCountRejectsWrongValue(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard ${{ matrix.shard }}
        env:
          SHARD_COUNT: 8
          SHARD_TARGET: ${{ matrix.shard }}
        run: bash hack/verify-archtest.sh
`)
	require.Error(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsMissingJob covers the
// completeness gap: yaml has no verify-archtest job at all (e.g. someone
// deleted it). Validator must fail loudly, not silently pass.
func TestVerifyArchtestCIExplicitShardCountRejectsMissingJob(t *testing.T) {
	body := []byte(`jobs:
  build-test:
    steps:
      - name: build
        run: go build ./...
`)
	require.Error(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountAcceptsCorrectShape is the GREEN
// fixture: minimum-required shape (job exists, step env has SHARD_COUNT=16).
// Documents the contract the real yaml must satisfy.
func TestVerifyArchtestCIExplicitShardCountAcceptsCorrectShape(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard ${{ matrix.shard }}
        env:
          SHARD_COUNT: 16
          SHARD_TARGET: ${{ matrix.shard }}
        run: bash hack/verify-archtest.sh
`)
	require.NoError(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsSiblingStepEnvShadow covers the
// PR-878 reviewer F1 gap: a dummy/setup step carries `SHARD_COUNT: 16` env
// (e.g. accidental copy-paste from a former invocation step), but the real
// `bash hack/verify-archtest.sh` step has no SHARD_COUNT env. GitHub Actions
// step env is step-scoped — sibling env does NOT propagate, so the real step
// would silently fall back to script default K=1 and OOM on GHA. The
// validator MUST reject this shape; pre-F1-fix code falsely accepted it.
func TestVerifyArchtestCIExplicitShardCountRejectsSiblingStepEnvShadow(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    steps:
      - name: Build slowgate
        env:
          SHARD_COUNT: 16
        run: go build -o "$RUNNER_TEMP/slowgate" ./tools/slowgate
      - name: Verify archtest shard ${{ matrix.shard }}
        env:
          SHARD_TARGET: ${{ matrix.shard }}
        run: bash hack/verify-archtest.sh
`)
	require.Error(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsNoInvocationStep covers the
// case where the verify-archtest job exists but no step actually invokes
// `hack/verify-archtest.sh` — e.g. someone renamed the step or split the
// script out without updating the gate. matrix gate is sole CI archtest
// entry (ADR §D6); silent removal must fail loudly.
func TestVerifyArchtestCIExplicitShardCountRejectsNoInvocationStep(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    steps:
      - name: Build slowgate
        env:
          SHARD_COUNT: 16
        run: go build -o "$RUNNER_TEMP/slowgate" ./tools/slowgate
      - name: Echo only
        env:
          SHARD_COUNT: 16
        run: echo "no script invocation"
`)
	require.Error(t, validateVerifyArchtestExplicitShardCount(body))
}

// archtestWorkflowConfig parses only the subset of _build-lint.yml needed
// for the verify-archtest SHARD_COUNT assertion. Decoupled from
// ci_pinning_test.go's workflowStep type so adding env doesn't risk
// pin-check regressions.
type archtestWorkflowConfig struct {
	Jobs map[string]archtestWorkflowJob `yaml:"jobs"`
}

type archtestWorkflowJob struct {
	Steps []archtestWorkflowStep `yaml:"steps"`
}

type archtestWorkflowStep struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

// validateVerifyArchtestExplicitShardCount enforces ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01:
// every step in jobs.verify-archtest that invokes `hack/verify-archtest.sh` must
// set `SHARD_COUNT=16` in **its own** `env:` block.
//
// Why bound to the running step, not any step: GitHub Actions step env applies
// only during that step's process. A dummy/setup step with `SHARD_COUNT: 16`
// in env never reaches the verify-archtest.sh execution context — relying on
// "any step has env" would let the real run step silently fall back to the
// script default (K=1 → 20 GB peak RSS → GHA OOM).
//
// Match-all semantics (not first-match): if multiple steps invoke the script,
// every one must satisfy the contract (defense-in-depth against future yaml
// refactors that split invocation across steps).
func validateVerifyArchtestExplicitShardCount(body []byte) error {
	var cfg archtestWorkflowConfig
	dec := yaml.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("parse archtest-nightly.yml: %w", err)
	}
	job, ok := cfg.Jobs["verify-archtest"]
	if !ok {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest missing from archtest-nightly.yml")
	}
	invocations := 0
	for _, step := range job.Steps {
		if !strings.Contains(step.Run, verifyArchtestScriptMarker) {
			continue
		}
		invocations++
		v, has := step.Env["SHARD_COUNT"]
		if !has {
			return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: step %q runs %s but its env "+
				"is missing SHARD_COUNT; CI must explicit set SHARD_COUNT=16 on the same step "+
				"(script default is 1 local-friendly; GHA step env is step-scoped — sibling step env does NOT apply). "+
				"See ADR 202605120000 §Amendment 2026-05-23.",
				step.Name, verifyArchtestScriptMarker)
		}
		if v != "16" {
			return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: step %q runs %s with SHARD_COUNT=%q; "+
				"CI must set SHARD_COUNT=16 (GHA 7 GB shard RSS budget; see ADR 202605120000 §Amendment 2026-05-23)",
				step.Name, verifyArchtestScriptMarker, v)
		}
	}
	if invocations == 0 {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest has no step running %s; "+
			"matrix gate is the sole CI archtest entry, removal is a regression (see ADR 202605120000 §D6)",
			verifyArchtestScriptMarker)
	}
	return nil
}
