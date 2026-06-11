//go:build archtest

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

// verifyArchtestCLIMarker is the substring that identifies a step actually
// invoking the gocell CLI's verify archtest subcommand. The validator MUST
// bind the --shard=N/K denominator assertion to the SAME step that runs the
// CLI, not to any sibling step. GitHub Actions step env is step-scoped — a
// sibling/setup step with the right values cannot satisfy the contract because
// its values never reach the actual CLI execution context.
// ref: GitHub Docs jobs.<job_id>.steps[*].env.
const verifyArchtestCLIMarker = "verify archtest"

// expectedShardTotal is the ADR-mandated shard denominator K for the CI matrix.
// The matrix array must enumerate exactly [0, 1, …, K-1], and every CLI
// invocation in the verify-archtest job must use --shard=N/K with this K.
//
// Raised from 16 to 24 per ADR 202605120000 §Amendment 2026-05-28:
// 4 days of nightly failures (2026-05-24..27) mixed slowgate breach + SIGTERM
// 143 with repeating-shard distribution suggesting OOM under-budgeted K=16
// baseline. K=24 stays in the direction of §Phase 0's "more shards = lower
// per-shard RSS" argument.
const expectedShardTotal = 24

// TestVerifyArchtestCIExplicitShardCount asserts that the verify-archtest
// CI job in .github/workflows/archtest-nightly.yml has:
//
//  1. A step that invokes `gocell verify archtest` (or the alias `gocell archtest`)
//     with a --shard=N/K flag where K == expectedShardTotal.
//  2. strategy.matrix.shard list == [0, 1, …, expectedShardTotal-1] so every
//     N value the matrix produces is actually executed.
//
// Background: archtestrunner.partition uses --shard=N/K to select a modulo
// subset of tests per shard. If K in the CLI invocation and the matrix array
// length diverge, some shards silently overlap (tests run twice) or gap
// (tests never run). This invariant prevents that class of regression.
//
// AI-robust: Medium runtime guard. The constraint "CLI verify archtest
// --shard=N/K denominator must match the matrix array length" cannot be
// bypassed without modifying this archtest in the same PR. Violation is
// reviewer-visible diff.
//
// The guard targets .github/workflows/archtest-nightly.yml — the sole
// authoritative CI gate for the archtest matrix (ADR 202605120000
// §Amendment 2026-05-23-pr-time-to-nightly). Local full-matrix feedback:
// `make verify` or `bash hack/verify-archtest.sh` (explicit trigger; pre-push
// archtest was retracted in PR #887 round-2, see ADR §"pre-push archtest 撤回").
//
// ref: ADR docs/architecture/202605120000-adr-archtest-process-isolation.md
// §Amendment 2026-05-23 + §Amendment 2026-05-23-pr-time-to-nightly
// + §Amendment 2026-05-28 (K=16→24)
// + §Amendment 2026-06-12 (#1563, CLI migration, SHARD_COUNT env removed).
func TestVerifyArchtestCIExplicitShardCount(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "workflows", "archtest-nightly.yml")))
	require.NoError(t, err)
	require.NoError(t, validateVerifyArchtestShardDenominator(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsMissingCLIStep is the negative
// fixture: a verify-archtest job whose step doesn't invoke the gocell CLI
// verify archtest must fail validation. Guards against future yaml refactor
// silently dropping the CLI invocation.
func TestVerifyArchtestCIExplicitShardCountRejectsMissingCLIStep(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
    steps:
      - name: Echo only
        run: echo "no CLI invocation"
`)
	require.Error(t, validateVerifyArchtestShardDenominator(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsWrongDenominator covers the
// denominator drift case: --shard=N/16 with a 24-entry matrix → fail.
// Using K=16 as the wrong value because it's a historically used production
// value distinct from the current expectedShardTotal=24.
func TestVerifyArchtestCIExplicitShardCountRejectsWrongDenominator(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/16 --timeout=5m
`)
	require.Error(t, validateVerifyArchtestShardDenominator(body))
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
	require.Error(t, validateVerifyArchtestShardDenominator(body))
}

// TestVerifyArchtestCIExplicitShardCountAcceptsCorrectShape is the GREEN
// fixture: minimum-required shape (job exists, strategy.matrix.shard covers
// [0..expectedShardTotal-1], step run contains --shard=N/24).
// Documents the contract the real yaml must satisfy.
func TestVerifyArchtestCIExplicitShardCountAcceptsCorrectShape(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24 --timeout=5m
`)
	require.NoError(t, validateVerifyArchtestShardDenominator(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsMissingMatrix covers the matrix
// gap: CLI step present but strategy.matrix.shard absent → no shard dispatch
// at all, gate silently dead.
func TestVerifyArchtestCIExplicitShardCountRejectsMissingMatrix(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24 --timeout=5m
`)
	require.Error(t, validateVerifyArchtestShardDenominator(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsMatrixLengthMismatch covers the
// realistic regression introduced by bumping expectedShardTotal without
// extending the matrix list: --shard=N/24 with matrix [0..15] would silently
// leave shards 16-23 executing tests that overlap with the lower-numbered shards
// (since the CLI's modulo is applied against K=24 but only 16 runners exist).
func TestVerifyArchtestCIExplicitShardCountRejectsMatrixLengthMismatch(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15]
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24 --timeout=5m
`)
	require.Error(t, validateVerifyArchtestShardDenominator(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsNonContiguousMatrix covers the
// case where the matrix has the right length but skips a value (e.g. someone
// hand-edits and deletes a row mid-list). The CLI computes --shard=N/K where
// N comes from matrix.shard, so any skipped index means some modulo partition
// is permanently unexecuted.
func TestVerifyArchtestCIExplicitShardCountRejectsNonContiguousMatrix(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 24]
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --shard=${{ matrix.shard }}/24 --timeout=5m
`)
	require.Error(t, validateVerifyArchtestShardDenominator(body))
}

// TestVerifyArchtestCIExplicitShardCountRejectsMissingShardFlag covers the
// case where the CLI step invokes `gocell verify archtest` but without any
// --shard flag. Without --shard, the CLI runs ALL tests on every matrix
// runner (no partitioning), defeating the shard fanout entirely.
func TestVerifyArchtestCIExplicitShardCountRejectsMissingShardFlag(t *testing.T) {
	body := []byte(`jobs:
  verify-archtest:
    strategy:
      matrix:
        shard: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23]
    steps:
      - name: Verify archtest shard
        run: |
          "$RUNNER_TEMP/gocell" verify archtest --timeout=5m
`)
	require.Error(t, validateVerifyArchtestShardDenominator(body))
}

// archtestWorkflowConfig parses only the subset of archtest-nightly.yml needed
// for the verify-archtest shard denominator assertion. Decoupled from
// ci_pinning_test.go's workflowStep type so adding fields doesn't risk
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
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

// extractShardDenominator parses the denominator K from a --shard=N/K flag in
// a step's run block. Returns 0 and an error if no --shard flag is found or the
// denominator is not a valid positive integer.
func extractShardDenominator(run string) (int, error) {
	// Match --shard=<anything>/<digits> or --shard <anything>/<digits>.
	// We use a simpler approach: find "--shard" then scan for "/N" pattern.
	idx := strings.Index(run, "--shard")
	if idx == -1 {
		return 0, fmt.Errorf("no --shard flag found in step run block")
	}
	// Extract the shard value after "--shard" (with = or space).
	after := run[idx+len("--shard"):]
	after = strings.TrimLeft(after, "= ")
	// The value is "N/K" where N may be a template expression.
	// Find the slash that separates N from K.
	slashIdx := strings.Index(after, "/")
	if slashIdx == -1 {
		return 0, fmt.Errorf("--shard flag found but no '/' separator: %q", after)
	}
	// K starts after the slash; read digits until whitespace/end.
	kStr := after[slashIdx+1:]
	end := strings.IndexAny(kStr, " \t\n\r\\\"')")
	if end != -1 {
		kStr = kStr[:end]
	}
	k, err := strconv.Atoi(kStr)
	if err != nil {
		return 0, fmt.Errorf("--shard denominator %q is not a valid integer: %w", kStr, err)
	}
	if k <= 0 {
		return 0, fmt.Errorf("--shard denominator must be > 0, got %d", k)
	}
	return k, nil
}

// validateVerifyArchtestShardDenominator enforces ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01
// in its new shape after CLI migration (#1563):
//
//  1. Every step in jobs.verify-archtest whose run block contains
//     `verify archtest` must use a --shard=N/K flag where K == expectedShardTotal.
//  2. The job's strategy.matrix.shard list must equal [0, 1, …, expectedShardTotal-1]
//     so every N produced by the matrix is actually executed.
//  3. The job must have at least one step invoking `verify archtest`.
//
// Why denominator must match matrix length: archtestrunner.partition uses
// --shard=N/K modulo arithmetic. If K != len(matrix.shard), some modulo
// bucket N may be executed by multiple runners (overlap) or by none (gap),
// silently corrupting test coverage.
//
// Match-all semantics (not first-match): if multiple steps invoke the CLI,
// every one must use the correct denominator (defense-in-depth against future
// yaml refactors that split invocation across steps).
func validateVerifyArchtestShardDenominator(body []byte) error {
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
		if !strings.Contains(step.Run, verifyArchtestCLIMarker) {
			continue
		}
		invocations++
		k, err := extractShardDenominator(step.Run)
		if err != nil {
			return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: step %q invokes %q "+
				"but the --shard flag is missing or malformed: %w; "+
				"CI must pass --shard=N/%d so the partition denominator matches the "+
				"matrix array length. See ADR 202605120000 §Amendment 2026-06-12 (#1563).",
				step.Name, verifyArchtestCLIMarker, err, expectedShardTotal)
		}
		if k != expectedShardTotal {
			return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: step %q invokes %q "+
				"with --shard denominator %d; expected %d to match the matrix array length "+
				"(mismatch causes tests to overlap or gap across shards). "+
				"See ADR 202605120000 §Amendment 2026-06-12 (#1563).",
				step.Name, verifyArchtestCLIMarker, k, expectedShardTotal)
		}
	}
	if invocations == 0 {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest has no step "+
			"invoking %q; matrix gate is the sole CI archtest entry, removal is a regression "+
			"(see ADR 202605120000 §D6). Add a step running `gocell verify archtest --shard=N/%d`.",
			verifyArchtestCLIMarker, expectedShardTotal)
	}
	return nil
}

// validateMatrixShardCoverage asserts that strategy.matrix.shard equals the
// contiguous sequence [0, 1, …, expectedShardTotal-1]. Mismatch ⇒ some
// --shard=N/K values the CLI can receive will never be exercised in CI,
// silently masking regressions on those shards.
func validateMatrixShardCoverage(shards []int) error {
	exp := expectedShardTotal
	if len(shards) == 0 {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest.strategy.matrix.shard "+
			"is empty; matrix must enumerate [0..%d] to cover every shard index produced under "+
			"--shard=N/%d (see ADR 202605120000 §Amendment 2026-05-28)", exp-1, exp)
	}
	if len(shards) != exp {
		return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest.strategy.matrix.shard "+
			"has length %d; expected %d to match --shard denominator %d (see ADR 202605120000 §Amendment 2026-05-28)",
			len(shards), exp, exp)
	}
	for i, v := range shards {
		if v != i {
			return fmt.Errorf("ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01: jobs.verify-archtest.strategy.matrix.shard[%d] "+
				"= %d; expected the contiguous sequence [0..%d] so every --shard=N/%d invocation covers "+
				"a distinct modulo bucket (see ADR 202605120000 §Amendment 2026-05-28)", i, v, exp-1, exp)
		}
	}
	return nil
}
