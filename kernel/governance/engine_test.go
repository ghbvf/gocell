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

// TestResolveNext covers the per-finding disposition derivation: severity
// default (error→block, warning→advisory) with an explicit override winning.
func TestResolveNext(t *testing.T) {
	t.Parallel()
	assert.Equal(t, NextBlock, resolveNext("", SeverityError))
	assert.Equal(t, NextAdvisory, resolveNext("", SeverityWarning))
	assert.Equal(t, NextAutofix, resolveNext(NextAutofix, SeverityError))
	assert.Equal(t, NextSuggest, resolveNext(NextSuggest, SeverityWarning))
}

// TestRuleStamp covers the engine's only post-processing: deriving each
// finding's Next from its severity (Rule.Next overriding) and passing through
// any per-finding Metric set by the Detect function.
func TestRuleStamp(t *testing.T) {
	t.Parallel()
	v := newTestValidator(t)
	base := Rule{Code: codeADV05, Phase: PhaseBase}

	t.Run("Next derived per finding from severity", func(t *testing.T) {
		got := base.stamp(v, []ValidationResult{
			v.newScopedError(codeADV05, IssueForbidden, "project", "f", "m", "fix"), // error
			v.newWarning(codeADV05, IssueForbidden, "", "f", "m", "fix"),            // warning
		})
		require.Len(t, got, 2)
		assert.Equal(t, NextBlock, got[0].Next)    // error → block
		assert.Equal(t, NextAdvisory, got[1].Next) // warning → advisory
		assert.Nil(t, got[0].Metric)
	})
	t.Run("explicit Next overrides the severity default", func(t *testing.T) {
		r := Rule{Code: codeADV05, Next: NextSuggest}
		got := r.stamp(v, v.scopedFindings(codeADV05)) // error finding
		require.Len(t, got, 1)
		assert.Equal(t, NextSuggest, got[0].Next) // override beats the block default
	})
	t.Run("stamp preserves detect-set per-finding Metric unchanged", func(t *testing.T) {
		// Detect sets Metric on specific findings; stamp() must pass it through.
		val := 42.0
		findings := []ValidationResult{
			v.newScopedError(codeADV05, IssueForbidden, "project", "f", "m", "fix"),
		}
		findings[0].Metric = &val
		got := base.stamp(v, findings)
		require.Len(t, got, 1)
		require.NotNil(t, got[0].Metric, "stamp must preserve detect-set Metric")
		assert.Equal(t, 42.0, *got[0].Metric, "Metric value must be unchanged after stamp")
	})
	t.Run("stamp does not set Metric when detect left it nil", func(t *testing.T) {
		got := base.stamp(v, v.scopedFindings(codeADV05))
		require.Len(t, got, 1)
		assert.Nil(t, got[0].Metric, "stamp must not inject a Metric when detect left it nil")
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
	assert.Equal(t, NextBlock, got[0].Next) // error finding → block
	assert.Equal(t, codeADV01, got[1].Code)
	assert.Equal(t, NextAdvisory, got[1].Next) // warning finding → advisory
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
