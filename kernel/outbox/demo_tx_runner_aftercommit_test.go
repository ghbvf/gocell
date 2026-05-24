package outbox_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence/persistencetest"
)

// TestDemoTxRunner_AfterCommitConformance asserts the demo (no-real-tx) runner
// still honors the after-commit hook contract — hooks fire after fn succeeds,
// not when it errors — so demo assemblies behave identically to durable ones.
func TestDemoTxRunner_AfterCommitConformance(t *testing.T) {
	persistencetest.RunAfterCommitConformance(t, outbox.DemoTxRunner{})
}
