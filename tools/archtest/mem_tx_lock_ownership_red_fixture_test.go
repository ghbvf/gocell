//go:build archtest

// INVARIANT: MEM-TX-LOCK-OWNERSHIP-01
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemTxLockOwnership01_FixturePattern is the MEM-TX-LOCK-OWNERSHIP-01
// reverse self-check (ai-robust.md §"工具选定后强制盲区自检"): a real,
// build-tag-gated package (internal/memtxlockfixture) modeling the sealed
// lock-lease funnel. The detector scanMemTxLockWitness MUST report exactly the
// three RED sites and MUST NOT flag the clean one:
//
//	W1 leakAcquire        — txlock.Acquire outside (memTxRunner).runLocked
//	W1 leakAcquireFuncLit — txlock.Acquire hidden in a closure (scan must descend)
//	W2 inLiveTx           — return drops the `l.Live(&s.mu)` delegation
//
// Clean (MUST NOT be flagged): runLocked's Acquire(&r.s.mu) + the Lease injected
// into ctx. Asserting the exact count catches both false-negative drift (detector
// goes blind) and false-positive drift (detector flags the sanctioned site). The
// former R1 (memTxToken literal scope) is gone: the ctx carries txlock.Lease
// directly and a live Lease can only come from Acquire (W1), so seal + W1 subsume
// it.
func TestMemTxLockOwnership01_FixturePattern(t *testing.T) {
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkgPath := modPath + "/tools/archtest/internal/memtxlockfixture"
	fixturePattern := "./tools/archtest/internal/memtxlockfixture/..."

	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{fixturePattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			return scanMemTxLockWitness(p)
		})

	for _, d := range diags {
		t.Log(d.Message)
	}
	require.Len(t, diags, 3,
		"%s reverse self-check: memtxlockfixture must yield exactly 3 RED sites "+
			"(W1 leakAcquire + W1 leakAcquireFuncLit + W2 inLiveTx); the sanctioned "+
			"runLocked site must not be flagged",
		ruleMemTxLockOwnership01)

	joined := ""
	for _, d := range diags {
		joined += d.Message + "\n"
	}
	// Each diagnostic names its enclosing function as "in <fn>"; assert the three
	// RED sites are reported there and the sanctioned site is not.
	assert.Contains(t, joined, "in leakAcquire", "W1: Acquire-outside-runLocked must be reported")
	assert.Contains(t, joined, "in leakAcquireFuncLit", "W1: Acquire hidden in a closure must be reported")
	assert.Contains(t, joined, "in inLiveTx", "W2: weakened inLiveTx form must be reported")
	assert.NotContains(t, joined, "in runLocked", "runLocked is the sanctioned Acquire site")
}
