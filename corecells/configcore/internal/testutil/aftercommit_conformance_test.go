package testutil

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/persistence/persistencetest"
)

// TestNoopTxRunner_AfterCommitConformance asserts the configcore test-util
// runner honors the after-commit hook contract so tests built on it observe
// the same hook timing as production runners.
func TestNoopTxRunner_AfterCommitConformance(t *testing.T) {
	persistencetest.RunAfterCommitConformance(t, &NoopTxRunner{})
}

// TestNoopTxRunner_AfterCommitNestedConformance covers the scope-discard
// semantics for nested RunInTx (success fires both; error discards inner scope).
func TestNoopTxRunner_AfterCommitNestedConformance(t *testing.T) {
	persistencetest.RunAfterCommitNestedConformance(t, &NoopTxRunner{})
}
