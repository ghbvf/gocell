// Package a contains test cases for the unconditionalskip analyzer.
package a

import "testing"

// case 1: Test fn first stmt is t.Skip("...") — should be reported.
func TestUnconditionalSkip(t *testing.T) {
	t.Skip("not implemented yet") // want "unconditional t\\.Skip"
	_ = 1 + 1
}

// case 2: Test fn first stmt is conditional skip — should NOT be reported.
func TestConditionalSkip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	_ = 1 + 1
}

// case 3: Test fn first stmt is t.Skipf("...") — should be reported.
func TestUnconditionalSkipf(t *testing.T) {
	t.Skipf("format %s", "reason") // want "unconditional t\\.Skip"
	_ = 1 + 1
}

// case 4: Test fn first stmt is t.SkipNow() — should be reported.
func TestUnconditionalSkipNow(t *testing.T) {
	t.SkipNow() // want "unconditional t\\.Skip"
	_ = 1 + 1
}

// case 5: t.Run sub-function with unconditional Skip — should be reported.
func TestRunWithUnconditionalSkip(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		t.Skip("skipping sub") // want "unconditional t\\.Skip"
	})
}

// case 6: TestMain(m *testing.M) — not *testing.T, should NOT be reported.
func TestMain(m *testing.M) {
	// TestMain does not have *testing.T; skip calls on *testing.M are irrelevant.
	m.Run()
}

// case 7: helper function (not Test prefix) — should NOT be reported.
func helperSkip(t *testing.T) {
	t.Skip("helper always skips")
}

// case 8: Skip is NOT the first statement — should NOT be reported.
func TestSkipNotFirst(t *testing.T) {
	_ = helperSkip // reference to avoid unused warning
	_ = 1 + 1
	t.Skip("skipping after work")
}

// case 9: Exact name "Test" (length == 4) with first stmt t.Skip — should be reported.
// Pin boundary: hasTestPrefix must accept len(name) == 4 (>= 4, not > 4).
func Test(t *testing.T) {
	t.Skip("boundary case — exact 4-char name") // want "unconditional t\\.Skip"
}
