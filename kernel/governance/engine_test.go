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

// scopedFindings builds a single scoped finding via the locator constructor so
// the engine tests exercise real ValidationResult values without raw literals.
func (v *Validator) scopedFindings(code RuleCode) []ValidationResult {
	return []ValidationResult{v.newScopedError(code, IssueForbidden, "project", "f", "m", "fix")}
}

// TestRunCollectsFindings verifies the single execution loop accumulates every
// rule's findings in order.
func TestRunCollectsFindings(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	rules := []Rule{
		{
			Code:   codeREF01,
			Detect: func(vv *Validator) []ValidationResult { return vv.scopedFindings(codeREF01) },
		},
		{
			Code: codeADV01,
			Detect: func(vv *Validator) []ValidationResult {
				return []ValidationResult{vv.newWarning(codeADV01, IssueRefNotFound, "", "b", "2", "y")}
			},
		},
	}
	got, err := v.run(context.Background(), rules, false)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, codeREF01, got[0].Code)
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, codeADV01, got[1].Code)
	assert.Equal(t, SeverityWarning, got[1].Severity)
}

// TestRunFailFast verifies failFast bails at the first SeverityError but a
// warning does not trigger the bailout.
func TestRunFailFast(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	warnFirst := Rule{
		Code: codeADV01,
		Detect: func(vv *Validator) []ValidationResult {
			return []ValidationResult{vv.newWarning(codeADV01, IssueRefNotFound, "", "w", "warn", "f")}
		},
	}
	errRule := Rule{
		Code:   codeREF01,
		Detect: func(vv *Validator) []ValidationResult { return vv.scopedFindings(codeREF01) },
	}
	never := Rule{
		Code: codeREF02,
		Detect: func(*Validator) []ValidationResult {
			t.Error("rule after first error must not run in fail-fast mode")
			return nil
		},
	}

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

// TestRunContextCancel_PartialFindings verifies that when ctx is canceled
// AFTER the first rule runs, run() returns the partial findings collected so
// far plus the context error. Callers use (len(findings)>0, err!=nil) to
// distinguish a clean run from an interrupted one.
func TestRunContextCancel_PartialFindings(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	ctx, cancel := context.WithCancel(context.Background())

	firstRan := false
	rules := []Rule{
		{
			Code: codeREF01,
			Detect: func(vv *Validator) []ValidationResult {
				firstRan = true
				// Cancel ctx inside the first rule's Detect; the loop check
				// fires before the second rule runs.
				cancel()
				return vv.scopedFindings(codeREF01)
			},
		},
		{
			Code: codeREF02,
			Detect: func(*Validator) []ValidationResult {
				t.Error("second rule must not run after ctx is canceled")
				return nil
			},
		},
	}

	got, err := v.run(ctx, rules, false)
	require.True(t, firstRan, "first rule must have run before cancellation check")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled),
		"error must be (or wrap) context.Canceled")
	require.Len(t, got, 1,
		"partial findings from the first rule must be returned alongside the error")
	assert.Equal(t, codeREF01, got[0].Code)
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
