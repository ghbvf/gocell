package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemTxLockOwnership01_FixturePattern is the MEM-TX-LOCK-OWNERSHIP-01
// reverse self-check (ai-robust.md §"工具选定后强制盲区自检"): a real,
// build-tag-gated package (internal/memtxlockfixture) modelling the sealed
// lock-witness funnel. The detector scanMemTxLockWitness MUST report exactly
// the three RED sites and MUST NOT flag the clean ones:
//
//	W1 leakAcquire   — txlock.Acquire outside (memTxRunner).runLocked
//	W2 txHoldsLock   — return drops `&& tok.held.Holds(&s.mu)`
//	R1 leakToken     — memTxToken literal outside runLocked / WithTxContext
//
// Clean (MUST NOT be flagged): runLocked's Acquire + memTxToken literal, and
// WithTxContext's memTxToken literal. Asserting the exact count catches both
// false-negative drift (detector goes blind) and false-positive drift
// (detector flags the sanctioned sites).
func TestMemTxLockOwnership01_FixturePattern(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkgPath := modPath + "/tools/archtest/internal/memtxlockfixture"
	fixturePattern := "./tools/archtest/internal/memtxlockfixture/..."

	diags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != fixturePkgPath {
				return nil // skip the txlock sub-package; scan the witness consumer
			}
			return scanMemTxLockWitness(p)
		})

	for _, d := range diags {
		t.Log(d.Message)
	}
	require.Len(t, diags, 3,
		"%s reverse self-check: memtxlockfixture must yield exactly 3 RED sites "+
			"(W1 leakAcquire + W2 txHoldsLock + R1 leakToken); the sanctioned "+
			"runLocked / WithTxContext sites must not be flagged",
		ruleMemTxLockOwnership01)

	joined := ""
	for _, d := range diags {
		joined += d.Message + "\n"
	}
	assert.Contains(t, joined, "leakAcquire", "W1: Acquire-outside-runLocked must be reported")
	assert.Contains(t, joined, "txHoldsLock", "W2: weakened txHoldsLock form must be reported")
	assert.Contains(t, joined, "leakToken", "R1: memTxToken literal outside sites must be reported")
	assert.NotContains(t, joined, "runLocked", "runLocked is the sanctioned Acquire + literal site")
	assert.NotContains(t, joined, "WithTxContext", "WithTxContext literal is sanctioned")
}
