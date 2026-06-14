package mem

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/persistence/persistencetest"
)

// TestMemTxRunner_AfterCommitConformance asserts the store-bound mem runner
// honors the after-commit hook contract. The runner holds store.mu for fn's
// duration; hooks fire after the locked section completes (analogous to PG
// firing after Commit while the connection is still checked out).
func TestMemTxRunner_AfterCommitConformance(t *testing.T) {
	persistencetest.RunAfterCommitConformance(t, NewStore(clock.Real()).TxRunner())
}
