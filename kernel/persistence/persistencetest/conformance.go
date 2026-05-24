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
	"slices"
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

// RunAfterCommitNestedConformance asserts the scope-discard semantics of nested
// RunInTx (the outermost-only drain plus per-scope rollback): a hook registered
// in a nested RunInTx whose fn ERRORS is discarded even when the outer fn
// swallows that error and commits — the nested unit of work (and the premise of
// its after-commit side effect) did not commit.
//
// Only runners that support nesting call this: pass-through runners (demo / noop
// / example) and postgres (savepoints). The mem runner holds a non-reentrant
// lock for fn's duration and cannot nest, so it is exempt.
func RunAfterCommitNestedConformance(t *testing.T, runner persistence.TxRunner) {
	t.Helper()

	t.Run("nested_success_fires_both_in_order", func(t *testing.T) {
		var order []string
		err := runner.RunInTx(context.Background(), func(outer context.Context) error {
			persistence.RegisterAfterCommit(outer, func(context.Context) { order = append(order, "outer") })
			return runner.RunInTx(outer, func(inner context.Context) error {
				persistence.RegisterAfterCommit(inner, func(context.Context) { order = append(order, "inner") })
				return nil
			})
		})
		if err != nil {
			t.Fatalf("nested success returned error: %v", err)
		}
		if !slices.Equal(order, []string{"outer", "inner"}) {
			t.Errorf("nested success fire order = %v, want [outer inner]", order)
		}
	})

	t.Run("nested_error_discards_inner_scope", func(t *testing.T) {
		var fired []string
		err := runner.RunInTx(context.Background(), func(outer context.Context) error {
			persistence.RegisterAfterCommit(outer, func(context.Context) { fired = append(fired, "outer") })
			// The inner unit fails and is rolled back; the outer swallows the
			// error and commits successfully.
			_ = runner.RunInTx(outer, func(inner context.Context) error {
				persistence.RegisterAfterCommit(inner, func(context.Context) { fired = append(fired, "inner") })
				return errors.New("nested unit failed")
			})
			return nil
		})
		if err != nil {
			t.Fatalf("outer returned error: %v", err)
		}
		if !slices.Equal(fired, []string{"outer"}) {
			t.Errorf("after nested rollback fired = %v, want [outer] (inner scope discarded)", fired)
		}
	})
}
