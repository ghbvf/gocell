// INVARIANT: ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01
package archtest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestVerifyArchtestCIExplicitShardCount asserts that the verify-archtest
// CI job in .github/workflows/_build-lint.yml sets SHARD_COUNT=16 explicitly
// in step env, rather than relying on the script default.
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
// ref: ADR docs/architecture/202605120000-adr-archtest-process-isolation.md
// §Amendment 2026-05-23.
func TestVerifyArchtestCIExplicitShardCount(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "workflows", "_build-lint.yml")))
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
}

// validateVerifyArchtestExplicitShardCount enforces ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01:
// jobs.verify-archtest must contain a step whose env has SHARD_COUNT exactly "16".
//
// The validator scans all steps in the job rather than locking to a specific
// step name — the canonical step is "Verify archtest shard ${{ matrix.shard }}"
// but the matrix interpolation makes string equality brittle. Any step under
// verify-archtest with `env: SHARD_COUNT: 16` satisfies the contract.
func validateVerifyArchtestExplicitShardCount(body []byte) error {
	var cfg archtestWorkflowConfig
	dec := yaml.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("parse _build-lint.yml: %w", err)
	}
	job, ok := cfg.Jobs["verify-archtest"]
	if !ok {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest missing from _build-lint.yml")
	}
	for _, step := range job.Steps {
		v, has := step.Env["SHARD_COUNT"]
		if !has {
			continue
		}
		if v != "16" {
			return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: step %q has SHARD_COUNT=%q; "+
				"CI must set SHARD_COUNT=16 (GHA 7 GB shard RSS budget; see ADR 202605120000 §Amendment 2026-05-23)",
				step.Name, v)
		}
		return nil
	}
	return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest steps[*].env.SHARD_COUNT missing; " +
		"hack/verify-archtest.sh default is 1 (local-friendly) — CI must explicit set SHARD_COUNT=16 " +
		"(GHA 7 GB shard RSS budget; see ADR 202605120000 §Amendment 2026-05-23)")
}
