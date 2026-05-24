package testutil

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/persistence/persistencetest"
)

// TestNoopTxRunner_AfterCommitConformance asserts the configcore test-util
// runner honors the after-commit hook contract so tests built on it observe
// the same hook timing as production runners.
func TestNoopTxRunner_AfterCommitConformance(t *testing.T) {
	persistencetest.RunAfterCommitConformance(t, &NoopTxRunner{})
}
