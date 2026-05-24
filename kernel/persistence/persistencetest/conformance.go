// Package persistencetest provides conformance suites for
// persistence.TxRunner implementations. Every TxRunner — postgres.TxManager,
// outbox.DemoTxRunner, cell-local mem runners, example demo runners — must
// pass RunAfterCommitConformance so the after-commit hook contract holds
// uniformly regardless of backend.
//
// This is the contract-fanout enforcement point (.claude/rules/gocell/
// contract-fanout.md 载体 2+3): the RunInTx contract now requires installing an
// after-commit registry and draining it after the outermost commit. A new
// TxRunner that forgets to wire this fails the suite.
//
// Assertions use the standard library only — kernel/ packages are isolated from
// testify by depguard, matching the sibling kernel conformance suites
// (kernel/outbox/outboxtest, kernel/command/commandtest).
package persistencetest

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/persistence"
)

// RunAfterCommitConformance asserts that runner honors the after-commit hook
// contract:
//
//   - a hook registered inside a RunInTx fn fires exactly once, AFTER the fn
//     body returns (i.e. post-commit, not inline); and
//   - a hook does not fire when fn returns an error (the tx is rolled back).
//
// Nesting / outermost-only semantics are runner-specific (savepoints vs direct
// pass-through; mem runners hold a non-reentrant lock for fn's duration) and
// are covered per-implementation, not in this universal suite.
func RunAfterCommitConformance(t *testing.T, runner persistence.TxRunner) {
	t.Helper()

	t.Run("fires_after_successful_commit", func(t *testing.T) {
		fired := 0
		err := runner.RunInTx(context.Background(), func(ctx context.Context) error {
			persistence.RegisterAfterCommit(ctx, func(context.Context) { fired++ })
			if fired != 0 {
				t.Errorf("hook ran inline within the tx body; got fired=%d, want 0", fired)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("RunInTx returned error on success path: %v", err)
		}
		if fired != 1 {
			t.Errorf("after-commit hook fired %d times, want exactly 1", fired)
		}
	})

	t.Run("skips_hook_when_fn_errors", func(t *testing.T) {
		sentinel := errors.New("conformance: deliberate fn error")
		fired := false
		err := runner.RunInTx(context.Background(), func(ctx context.Context) error {
			persistence.RegisterAfterCommit(ctx, func(context.Context) { fired = true })
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Errorf("RunInTx error = %v, want sentinel %v", err, sentinel)
		}
		if fired {
			t.Error("after-commit hook fired on a rolled-back tx; want no fire")
		}
	})
}
