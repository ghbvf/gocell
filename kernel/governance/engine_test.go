package governance

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
)

// newTestValidator returns a Validator over an empty project.
func newTestValidator(t *testing.T) *Validator {
	t.Helper()
	return NewValidator(nil, "", clock.Real())
}

func floatPtr(f float64) *float64 { return &f }

// scopedFindings builds n scoped findings via the locator constructor so the
// engine tests exercise real ValidationResult values without raw literals.
func (v *Validator) scopedFindings(code RuleCode, n int) []ValidationResult {
	out := make([]ValidationResult, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, v.newScopedError(code, IssueForbidden, "project", "f", "m", "fix"))
	}
	return out
}

// TestRuleStamp covers the engine's only post-processing: stamping Next on every
// finding and attaching the evaluated Metric (nil when absent or inapplicable).
func TestRuleStamp(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	base := Rule{Code: codeADV05, Phase: PhaseBase, Next: NextAdvisory}

	t.Run("stamps Next on all findings", func(t *testing.T) {
		got := base.stamp(v, v.scopedFindings(codeADV05, 3))
		require.Len(t, got, 3)
		for _, r := range got {
			assert.Equal(t, NextAdvisory, r.Next)
			assert.Nil(t, r.Metric)
		}
	})
	t.Run("applicable metric → value on every finding", func(t *testing.T) {
		r := base
		r.Metric = func(*Validator) (float64, bool) { return 12, true }
		got := r.stamp(v, v.scopedFindings(codeADV05, 2))
		require.Len(t, got, 2)
		for _, res := range got {
			assert.Equal(t, floatPtr(12), res.Metric)
		}
	})
	t.Run("inapplicable metric → nil", func(t *testing.T) {
		r := base
		r.Metric = func(*Validator) (float64, bool) { return 0, false }
		got := r.stamp(v, v.scopedFindings(codeADV05, 1))
		require.Len(t, got, 1)
		assert.Nil(t, got[0].Metric)
	})
	t.Run("empty findings → empty", func(t *testing.T) {
		assert.Empty(t, base.stamp(v, nil))
	})
}

// TestRunCollectsFindings verifies the single execution loop accumulates every
// rule's findings in order and stamps Next.
func TestRunCollectsFindings(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	rules := []Rule{
		{Code: codeREF01, Next: NextBlock,
			Detect: func(vv *Validator) []ValidationResult { return vv.scopedFindings(codeREF01, 1) }},
		{Code: codeADV01, Next: NextAdvisory,
			Detect: func(vv *Validator) []ValidationResult { return vv.scopedFindings(codeADV01, 2) }},
	}
	got, err := v.run(context.Background(), rules, false)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, codeREF01, got[0].Code)
	assert.Equal(t, NextBlock, got[0].Next)
	assert.Equal(t, codeADV01, got[1].Code)
	assert.Equal(t, NextAdvisory, got[2].Next)
}

// TestRunFailFast verifies failFast bails at the first SeverityError but a
// warning does not trigger the bailout.
func TestRunFailFast(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	warnFirst := Rule{Code: codeADV01, Next: NextAdvisory,
		Detect: func(vv *Validator) []ValidationResult {
			return []ValidationResult{vv.newWarning(codeADV01, IssueRefNotFound, "", "w", "warn", "f")}
		}}
	errRule := Rule{Code: codeREF01, Next: NextBlock,
		Detect: func(vv *Validator) []ValidationResult { return vv.scopedFindings(codeREF01, 1) }}
	never := Rule{Code: codeREF02, Next: NextBlock,
		Detect: func(*Validator) []ValidationResult {
			t.Error("rule after first error must not run in fail-fast mode")
			return nil
		}}

	got, err := v.run(context.Background(), []Rule{warnFirst, errRule, never}, true)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, SeverityWarning, got[0].Severity)
	assert.Equal(t, SeverityError, got[1].Severity)
}

// TestRunContextCancel verifies ctx cancellation returns partial findings plus
// the context error.
func TestRunContextCancel(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := v.run(ctx, []Rule{
		{Code: codeREF01, Detect: func(*Validator) []ValidationResult { return nil }},
	}, false)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Empty(t, got)
}

// TestFilterByPhase verifies phase selection keeps only matching rules and
// preserves declaration order.
func TestFilterByPhase(t *testing.T) {
	t.Parallel()
	rules := []Rule{
		{Code: codeREF01, Phase: PhaseBase},
		{Code: codeDEP01, Phase: PhaseDep},
		{Code: codeFMT16, Phase: PhaseStrict},
		{Code: codeCH01, Phase: PhaseHealth},
		{Code: codeREF02, Phase: PhaseBase},
	}
	t.Run("validate = base+dep", func(t *testing.T) {
		got := filterByPhase(rules, PhaseBase, PhaseDep)
		assert.Equal(t, []RuleCode{codeREF01, codeDEP01, codeREF02}, codesOf(got))
	})
	t.Run("validate strict = base+dep+strict", func(t *testing.T) {
		got := filterByPhase(rules, PhaseBase, PhaseDep, PhaseStrict)
		assert.Equal(t, []RuleCode{codeREF01, codeDEP01, codeFMT16, codeREF02}, codesOf(got))
	})
	t.Run("check = health only", func(t *testing.T) {
		got := filterByPhase(rules, PhaseHealth)
		assert.Equal(t, []RuleCode{codeCH01}, codesOf(got))
	})
}

func codesOf(rules []Rule) []RuleCode {
	out := make([]RuleCode, len(rules))
	for i := range rules {
		out[i] = rules[i].Code
	}
	return out
}
