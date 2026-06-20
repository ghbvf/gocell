package archtestrunner

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/scoperules"
)

// TestResolveScope_Valid covers the accepted scope values: empty + "workspace"
// both normalize to ScopeWorkspace (back-compat default), "framework" to
// ScopeFramework.
func TestResolveScope_Valid(t *testing.T) {
	tests := []struct {
		in   Scope
		want Scope
	}{
		{in: "", want: ScopeWorkspace},
		{in: ScopeWorkspace, want: ScopeWorkspace},
		{in: ScopeFramework, want: ScopeFramework},
	}
	for _, tc := range tests {
		t.Run(string(tc.in), func(t *testing.T) {
			got, err := ResolveScope(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestResolveScope_Invalid: unknown values are rejected (case-sensitive), with
// the offending value surfaced.
func TestResolveScope_Invalid(t *testing.T) {
	for _, in := range []Scope{"bogus", "WORKSPACE", "Framework", "all"} {
		t.Run(string(in), func(t *testing.T) {
			_, err := ResolveScope(in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), string(in), "error must name the bad scope value")
		})
	}
}

// TestSelectByScope intersects a discovered list with an allow-set, preserving
// discovery order and dropping anything outside the set.
func TestSelectByScope(t *testing.T) {
	discovered := []string{"TestA", "TestB", "TestC", "TestD"}
	allow := []string{"TestC", "TestA"}
	got := selectByScope(discovered, allow)
	assert.Equal(t, []string{"TestA", "TestC"}, got, "must preserve discovery order, drop non-members")

	assert.Empty(t, selectByScope(discovered, nil), "empty allow-set selects nothing")
}

// TestRuleFuncsForIDs_ZeroFuncGuard: an ID present in the request set but with
// no test func in the index is a fail-loud integrity error naming the orphan,
// not a silent drop (which would be a false-green framework run).
func TestRuleFuncsForIDs_ZeroFuncGuard(t *testing.T) {
	idx := ruleIndex{
		"FOO-01": {"TestFoo"},
		"BAR-01": {"TestBar", "TestBarTwo"},
	}
	t.Run("all ids resolve → union", func(t *testing.T) {
		got, err := ruleFuncsForIDs(idx, []string{"FOO-01", "BAR-01"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"TestFoo", "TestBar", "TestBarTwo"}, got)
	})
	t.Run("orphan id → fail-loud naming it", func(t *testing.T) {
		_, err := ruleFuncsForIDs(idx, []string{"FOO-01", "MISSING-99"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MISSING-99", "error must name the orphan rule ID")
	})
}

// TestFrameworkTestFuncs_RealIDs wires frameworkTestFuncs against a fake archtest
// dir that anchors every scoperules.FrameworkRuleIDs entry, so the test adapts as
// the portable set grows. It proves the leaf set drives the runner's selection.
func TestFrameworkTestFuncs_RealIDs(t *testing.T) {
	require.NotEmpty(t, scoperules.FrameworkRuleIDs(), "framework rule set must be non-empty")

	files := map[string]string{}
	wantFuncs := make([]string, 0, len(scoperules.FrameworkRuleIDs()))
	for i, id := range scoperules.FrameworkRuleIDs() {
		fn := fmt.Sprintf("TestFrameworkRule%d", i)
		wantFuncs = append(wantFuncs, fn)
		files[fmt.Sprintf("rule_%d_test.go", i)] = fmt.Sprintf(
			"//go:build archtest\n\n// INVARIANT: %s\npackage archtest\nimport \"testing\"\nfunc %s(t *testing.T) {}\n",
			id, fn,
		)
	}
	root := makeFakeArchtestDir(t, files)

	got, err := frameworkTestFuncs(root)
	require.NoError(t, err)
	assert.ElementsMatch(t, wantFuncs, got, "framework funcs must cover every scoperules ID's anchor")
}

// TestBuildFrameworkFuncIndex_ConsolidatedFile proves the func-level attribution:
// in a theme-consolidated file the inventory header (a multi-ID list) is skipped
// and each rule gets ONLY the funcs under its own section anchor — not the whole
// file. A consolidation/helper test above the first section is unattributed.
func TestBuildFrameworkFuncIndex_ConsolidatedFile(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"theme_test.go": "//go:build archtest\n\n" +
			"// Theme file.\n//   - INVARIANT: ALPHA-01\n//   - INVARIANT: BETA-01\n" +
			"package archtest\n\nimport \"testing\"\n\n" +
			"func TestConsolidationHelper(t *testing.T) {}\n\n" + // before any section → unattributed
			"// INVARIANT: ALPHA-01\nfunc TestAlphaOne(t *testing.T) {}\n\n" +
			"func TestAlphaTwo(t *testing.T) {}\n\n" +
			"// INVARIANT: BETA-01\nfunc TestBetaOne(t *testing.T) {}\n",
	})

	idx, err := buildFrameworkFuncIndex(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"TestAlphaOne", "TestAlphaTwo"}, idx["ALPHA-01"],
		"ALPHA-01 must own only its section's funcs, not the whole file")
	assert.Equal(t, []string{"TestBetaOne"}, idx["BETA-01"])
	assert.NotContains(t, idx["ALPHA-01"], "TestConsolidationHelper",
		"a func above the first section anchor is unattributed (not leaked into a rule)")
	assert.NotContains(t, idx["BETA-01"], "TestConsolidationHelper")
}

// TestApplyFilters_FrameworkScopeNarrows: framework scope keeps only the
// framework rule funcs (and that narrowing happens before sharding); workspace
// scope is a pass-through.
func TestApplyFilters_FrameworkScopeNarrows(t *testing.T) {
	files := map[string]string{}
	frameworkFuncs := make([]string, 0, len(scoperules.FrameworkRuleIDs()))
	for i, id := range scoperules.FrameworkRuleIDs() {
		fn := fmt.Sprintf("TestFrameworkRule%d", i)
		frameworkFuncs = append(frameworkFuncs, fn)
		files[fmt.Sprintf("rule_%d_test.go", i)] = fmt.Sprintf(
			"//go:build archtest\n\n// INVARIANT: %s\npackage archtest\nimport \"testing\"\nfunc %s(t *testing.T) {}\n",
			id, fn,
		)
	}
	root := makeFakeArchtestDir(t, files)
	e := engine{exec: defaultExec, changed: changedRepoFiles}

	// discovered = framework funcs + workspace-only extras.
	discovered := append([]string{"TestWorkspaceOnlyA", "TestWorkspaceOnlyB"}, frameworkFuncs...)

	t.Run("framework narrows to framework funcs", func(t *testing.T) {
		got, err := e.applyFilters(t.Context(), Request{WorkspaceRoot: root, Scope: ScopeFramework}, discovered)
		require.NoError(t, err)
		assert.ElementsMatch(t, frameworkFuncs, got)
		assert.NotContains(t, got, "TestWorkspaceOnlyA")
	})

	t.Run("workspace is pass-through", func(t *testing.T) {
		got, err := e.applyFilters(t.Context(), Request{WorkspaceRoot: root, Scope: ScopeWorkspace}, discovered)
		require.NoError(t, err)
		assert.ElementsMatch(t, discovered, got)
	})
}

// TestApplyFilters_FrameworkRuleOutOfScope: --scope=framework with --rule naming
// a workspace-only (gocell-internal) rule must fail loud, not return an empty
// selection (a vacuous-green run).
func TestApplyFilters_FrameworkRuleOutOfScope(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"layer_test.go": "//go:build archtest\n\n// INVARIANT: LAYER-05\npackage archtest\nimport \"testing\"\nfunc TestLayer(t *testing.T) {}\n",
	})
	e := engine{exec: defaultExec, changed: changedRepoFiles}
	_, err := e.applyFilters(t.Context(),
		Request{WorkspaceRoot: root, Scope: ScopeFramework, Rule: "LAYER-05"},
		[]string{"TestLayer"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LAYER-05")
	assert.True(t, strings.Contains(err.Error(), "framework"),
		"error must explain the rule is outside framework scope, got: %q", err.Error())
}

// TestRun_UnknownScopeFailClosed: the Go API (not just the CLI) rejects an
// unknown scope before doing any work — Run/ListTests must not silently treat a
// bogus scope as workspace.
func TestRun_UnknownScopeFailClosed(t *testing.T) {
	e := engine{
		exec: func(_ context.Context, _ string, _, _ []string) ([]byte, error) {
			panic("exec must not run on bad scope")
		},
		changed: changedRepoFiles,
	}
	_, lerr := e.listTests(t.Context(), Request{WorkspaceRoot: t.TempDir(), Scope: "bogus"})
	require.Error(t, lerr)
	assert.Contains(t, lerr.Error(), "bogus")

	_, rerr := e.run(t.Context(), Request{WorkspaceRoot: t.TempDir(), Scope: "bogus"})
	require.Error(t, rerr)
	assert.Contains(t, rerr.Error(), "bogus")
}

// TestIsFrameworkRule covers both membership branches of the gate used by
// --scope=framework --rule.
func TestIsFrameworkRule(t *testing.T) {
	assert.True(t, isFrameworkRule(scoperules.PanicRegistered01), "a registered framework rule is in scope")
	assert.False(t, isFrameworkRule("LAYER-05"), "a workspace-only rule is not in scope")
	assert.False(t, isFrameworkRule(""), "empty rule id is not in scope")
}

// TestApplyFilters_FrameworkRuleInScope: --scope=framework with a --rule that IS
// in the framework set passes applyScope and narrows to that rule's funcs (the
// positive counterpart of TestApplyFilters_FrameworkRuleOutOfScope). The fake dir
// anchors EVERY framework rule because applyScope resolves the whole set (a
// partial dir would trip the zero-func guard on the unanchored rules).
func TestApplyFilters_FrameworkRuleInScope(t *testing.T) {
	ids := scoperules.FrameworkRuleIDs()
	files := map[string]string{}
	for i, id := range ids {
		files[fmt.Sprintf("rule_%d_test.go", i)] = fmt.Sprintf(
			"//go:build archtest\n\n// INVARIANT: %s\npackage archtest\nimport \"testing\"\nfunc TestFrameworkRule%d(t *testing.T) {}\n",
			id, i,
		)
	}
	root := makeFakeArchtestDir(t, files)
	e := engine{exec: defaultExec, changed: changedRepoFiles}
	// Rule = the first framework rule; its func is TestFrameworkRule0.
	got, err := e.applyFilters(t.Context(),
		Request{WorkspaceRoot: root, Scope: ScopeFramework, Rule: ids[0]},
		[]string{"TestFrameworkRule0", "TestOther"})
	require.NoError(t, err)
	assert.Equal(t, []string{"TestFrameworkRule0"}, got)
}

// TestBuildFrameworkFuncIndex_AnchorWithProse locks the heuristic: a comment group
// that declares exactly one INVARIANT ID is a section anchor even with trailing
// prose, and the following func is attributed to it. (Documents the
// "single-ID comment group == anchor" assumption in buildFrameworkFuncIndex.)
func TestBuildFrameworkFuncIndex_AnchorWithProse(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"prose_test.go": "//go:build archtest\n\npackage archtest\n\nimport \"testing\"\n\n" +
			"// INVARIANT: GAMMA-01\n// Extra prose explaining the rule; still one anchor.\n" +
			"func TestGamma(t *testing.T) {}\n",
	})
	idx, err := buildFrameworkFuncIndex(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"TestGamma"}, idx["GAMMA-01"])
}

// TestBuildFrameworkFuncIndex_NonexistentDir: a missing tools/archtest dir yields
// an empty index without error (parity with buildRuleIndex), so discovery — not
// the index builder — owns the "no archtest" failure.
func TestBuildFrameworkFuncIndex_NonexistentDir(t *testing.T) {
	idx, err := buildFrameworkFuncIndex(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, idx)
}

// TestApplyFilters_FrameworkRuleFuncLevel: under --scope=framework, the --rule
// filter must use the FUNC-LEVEL index — when two framework rules share a
// theme-consolidated file, --rule on one must NOT leak the other's funcs (the
// file-level index that backs workspace --rule would conflate them). Regression
// for the codex F1 finding (PR #2478).
func TestApplyFilters_FrameworkRuleFuncLevel(t *testing.T) {
	ids := scoperules.FrameworkRuleIDs()
	require.GreaterOrEqual(t, len(ids), 2, "need ≥2 framework rules to co-locate")

	// ids[0] and ids[1] share one consolidated file (separate section anchors);
	// the rest get one file each so frameworkTestFuncs resolves the whole set.
	files := map[string]string{
		"consolidated_test.go": "//go:build archtest\n\n" +
			"// Theme.\n//   - INVARIANT: " + ids[0] + "\n//   - INVARIANT: " + ids[1] + "\n" +
			"package archtest\n\nimport \"testing\"\n\n" +
			"// INVARIANT: " + ids[0] + "\nfunc TestRule0A(t *testing.T) {}\n\n" +
			"// INVARIANT: " + ids[1] + "\nfunc TestRule1A(t *testing.T) {}\n",
	}
	for i := 2; i < len(ids); i++ {
		files[fmt.Sprintf("rule_%d_test.go", i)] = fmt.Sprintf(
			"//go:build archtest\n\n// INVARIANT: %s\npackage archtest\nimport \"testing\"\nfunc TestFrameworkRule%d(t *testing.T) {}\n",
			ids[i], i,
		)
	}
	root := makeFakeArchtestDir(t, files)
	e := engine{exec: defaultExec, changed: changedRepoFiles}

	got, err := e.applyFilters(t.Context(),
		Request{WorkspaceRoot: root, Scope: ScopeFramework, Rule: ids[0]},
		[]string{"TestRule0A", "TestRule1A"})
	require.NoError(t, err)
	assert.Equal(t, []string{"TestRule0A"}, got,
		"framework --rule must select only the rule's own section func, not the co-file framework rule")
}
