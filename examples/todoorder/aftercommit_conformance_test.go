package main

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/persistence/persistencetest"
)

// TestDemoTxRunner_AfterCommitConformance asserts the todoorder demo runner
// honors the after-commit hook contract, matching durable assemblies.
func TestDemoTxRunner_AfterCommitConformance(t *testing.T) {
	persistencetest.RunAfterCommitConformance(t, demoTxRunner{})
}

// TestDemoTxRunner_AfterCommitNestedConformance covers the scope-discard
// semantics for nested RunInTx (success fires both; error discards inner scope).
func TestDemoTxRunner_AfterCommitNestedConformance(t *testing.T) {
	persistencetest.RunAfterCommitNestedConformance(t, demoTxRunner{})
}
