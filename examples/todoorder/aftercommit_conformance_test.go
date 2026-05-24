package main

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/persistence/persistencetest"
)

// TestDemoTxRunner_AfterCommitConformance asserts the todoorder demo runner
// honors the after-commit hook contract, matching durable assemblies.
func TestDemoTxRunner_AfterCommitConformance(t *testing.T) {
	persistencetest.RunAfterCommitConformance(t, demoTxRunner{})
}
