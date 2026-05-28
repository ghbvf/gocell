// INVARIANT: ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01
package archtest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// verifyArchtestScriptMarker is the substring that identifies a step actually
// invoking the verify-archtest script. GitHub Actions step env (`steps[*].env`)
// only applies during that specific step's process. The validator MUST bind
// the SHARD_COUNT=24 assertion to the SAME step that runs this script — a
// dummy/setup step with SHARD_COUNT=24 in env cannot satisfy the contract
// because its env never reaches the actual `bash hack/verify-archtest.sh`
// execution. ref: GitHub Docs jobs.<job_id>.steps[*].env.
const verifyArchtestScriptMarker = "hack/verify-archtest.sh"

// expectedShardCount is the ADR-mandated SHARD_COUNT for the CI matrix.
// Raised from 16 to 24 per ADR 202605120000 §Amendment 2026-05-28: 4 days of
// nightly failures (2026-05-24..27) mixed slowgate breach + SIGTERM 143 with
// repeating-shard distribution suggesting OOM (ADR §11 lineage) under-budgeted
// K=16 baseline. K=24 stays in the direction of §Phase 0's "more shards =
// lower per-shard RSS" argument (K=16 baseline is macOS measurement, GHA Linux
// baseline pending Phase A diagnostic step capture).
const expectedShardCount = "24"

// TestVerifyArchtestCIExplicitShardCount asserts that the verify-archtest
// CI job in .github/workflows/archtest-nightly.yml sets SHARD_COUNT=24
// explicitly in step env, rather than relying on the script default.
//
// Background: hack/verify-archtest.sh default SHARD_COUNT is 1 (local-friendly:
// single process, ~300 Test* share *types.Info cache, lowest CPU). CI must set
// SHARD_COUNT=24 explicitly — GHA 2-core 7 GB shard runner cannot survive a
// single-process run that accumulates 20+ GB peak RSS (PR #445 OOM SIGTERM).
// If CI yaml drops the explicit value, the script falls through to K=1 and
// the next CI run blows up.
//
// AI-robust: Medium runtime guard. The constraint "CI yaml verify-archtest
// must explicit SHARD_COUNT=24" cannot be bypassed without modifying this
// archtest in the same PR. Violation is reviewer-visible diff.
//
// The guard targets .github/workflows/archtest-nightly.yml — the sole
// authoritative CI gate for the archtest matrix (ADR 202605120000
// §Amendment 2026-05-23-pr-time-to-nightly). Local full-matrix feedback:
// `make verify` or `bash hack/verify-archtest.sh` (explicit trigger; pre-push
// archtest was retracted in PR #887 round-2, see ADR §"pre-push archtest 撤回").
//
// ref: ADR docs/architecture/202605120000-adr-archtest-process-isolation.md
// §Amendment 2026-05-23 + §Amendment 2026-05-23-pr-time-to-nightly
// + §Amendment 2026-05-28 (K=16→24).
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
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
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
// drift case: env present but value != expectedShardCount. Fixture uses
// SHARD_COUNT=8, a value never used in production (K=6/K=8 are macOS Phase 0
// historical measurements only) — this keeps the drift fixture decoupled
// from any past or future K value, so future K-upgrade PRs only need to bump
// expectedShardCount without touching this fixture.
func TestVerifyArchtestCIExplicitShardCountRejectsWrongValue(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
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
// fixture: minimum-required shape (job exists, strategy.matrix.shard covers
// [0..expectedShardCount-1], step env has SHARD_COUNT=24). Documents the
// contract the real yaml must satisfy.
func TestVerifyArchtestCIExplicitShardCountAcceptsCorrectShape(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
    steps:
      - name: Verify archtest shard ${{ matrix.shard }}
        env:
          SHARD_COUNT: 24
          SHARD_TARGET: ${{ matrix.shard }}
        run: bash hack/verify-archtest.sh
`)
	require.NoError(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsMissingMatrix covers the matrix
// gap: SHARD_COUNT present but strategy.matrix.shard absent → no shard
// dispatch at all, gate silently dead.
func TestVerifyArchtestCIExplicitShardCountRejectsMissingMatrix(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard ${{ matrix.shard }}
        env:
          SHARD_COUNT: 24
          SHARD_TARGET: ${{ matrix.shard }}
        run: bash hack/verify-archtest.sh
`)
	require.Error(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsMatrixLengthMismatch covers the
// realistic regression introduced by bumping SHARD_COUNT without extending the
// matrix list: SHARD_COUNT=24 paired with matrix [0..15] would silently skip
// shards 16-23 (8 shards' tests never executed).
func TestVerifyArchtestCIExplicitShardCountRejectsMatrixLengthMismatch(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15]
    steps:
      - name: Verify archtest shard ${{ matrix.shard }}
        env:
          SHARD_COUNT: 24
          SHARD_TARGET: ${{ matrix.shard }}
        run: bash hack/verify-archtest.sh
`)
	require.Error(t, validateVerifyArchtestExplicitShardCount(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsNonContiguousMatrix covers the
// case where the matrix has the right length but skips a value (e.g. someone
// hand-edits and deletes a row mid-list). The script computes SHARD_TARGET as
// modulo over [0, SHARD_COUNT), so any skipped index leaves its tests
// permanently unexecuted.
func TestVerifyArchtestCIExplicitShardCountRejectsNonContiguousMatrix(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 24]
    steps:
      - name: Verify archtest shard ${{ matrix.shard }}
        env:
          SHARD_COUNT: 24
          SHARD_TARGET: ${{ matrix.shard }}
        run: bash hack/verify-archtest.sh
`)
	require.Error(t, validateVerifyArchtestExplicitShardCount(body))
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
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
    steps:
      - name: Build slowgate
        env:
          SHARD_COUNT: 24
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
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
    steps:
      - name: Build slowgate
        env:
          SHARD_COUNT: 24
        run: go build -o "$RUNNER_TEMP/slowgate" ./tools/slowgate
      - name: Echo only
        env:
          SHARD_COUNT: 24
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
	Strategy archtestWorkflowStrategy `yaml:"strategy"`
	Steps    []archtestWorkflowStep   `yaml:"steps"`
}

type archtestWorkflowStrategy struct {
	Matrix archtestWorkflowMatrix `yaml:"matrix"`
}

type archtestWorkflowMatrix struct {
	Shard []int `yaml:"shard"`
}

type archtestWorkflowStep struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

// validateVerifyArchtestExplicitShardCount enforces ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01:
//
//  1. Every step in jobs.verify-archtest that invokes `hack/verify-archtest.sh`
//     must set `SHARD_COUNT=<expectedShardCount>` in **its own** `env:` block.
//  2. The job's `strategy.matrix.shard` list must equal
//     `[0, 1, …, expectedShardCount-1]` so every SHARD_TARGET produced by the
//     matrix is actually executed.
//
// Why bound to the running step, not any step: GitHub Actions step env applies
// only during that step's process. A dummy/setup step with `SHARD_COUNT: 24`
// in env never reaches the verify-archtest.sh execution context — relying on
// "any step has env" would let the real run step silently fall back to the
// script default (K=1 → 20 GB peak RSS → GHA OOM).
//
// Why matrix is validated: without (2), bumping SHARD_COUNT from 16 to 24
// while leaving `matrix.shard: [0..15]` would silently skip shards 16-23
// (8 shards' tests never executed) and let regressions land unnoticed.
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
	if err := validateMatrixShardCoverage(job.Strategy.Matrix.Shard); err != nil {
		return err
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
				"is missing SHARD_COUNT; CI must explicit set SHARD_COUNT=%s on the same step "+
				"(script default is 1 local-friendly; GHA step env is step-scoped — sibling step env does NOT apply). "+
				"See ADR 202605120000 §Amendment 2026-05-23 + §Amendment 2026-05-28.",
				step.Name, verifyArchtestScriptMarker, expectedShardCount)
		}
		if v != expectedShardCount {
			return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: step %q runs %s with SHARD_COUNT=%q; "+
				"CI must set SHARD_COUNT=%s (GHA 7 GB shard RSS budget; see ADR 202605120000 §Amendment 2026-05-28)",
				step.Name, verifyArchtestScriptMarker, v, expectedShardCount)
		}
	}
	if invocations == 0 {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest has no step running %s; "+
			"matrix gate is the sole CI archtest entry, removal is a regression (see ADR 202605120000 §D6)",
			verifyArchtestScriptMarker)
	}
	return nil
}

// validateMatrixShardCoverage asserts that strategy.matrix.shard equals the
// contiguous sequence [0, 1, …, expectedShardCount-1]. Mismatch ⇒ some
// SHARD_TARGET indices the script can produce will never be exercised in CI,
// silently masking regressions on those shards.
func validateMatrixShardCoverage(shards []int) error {
	exp, err := strconv.Atoi(expectedShardCount)
	if err != nil {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: invalid expectedShardCount %q: %w",
			expectedShardCount, err)
	}
	if len(shards) == 0 {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest.strategy.matrix.shard "+
			"is empty; matrix must enumerate [0..%d] to cover every SHARD_TARGET produced under "+
			"SHARD_COUNT=%s (see ADR 202605120000 §Amendment 2026-05-28)", exp-1, expectedShardCount)
	}
	if len(shards) != exp {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest.strategy.matrix.shard "+
			"has length %d; expected %d to match SHARD_COUNT=%s (see ADR 202605120000 §Amendment 2026-05-28)",
			len(shards), exp, expectedShardCount)
	}
	for i, v := range shards {
		if v != i {
			return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest.strategy.matrix.shard[%d] "+
				"= %d; expected the contiguous sequence [0..%d] so every SHARD_TARGET is covered "+
				"(see ADR 202605120000 §Amendment 2026-05-28)", i, v, exp-1)
		}
	}
	return nil
}
