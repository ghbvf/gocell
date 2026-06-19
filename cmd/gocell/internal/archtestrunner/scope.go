package archtestrunner

import (
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/tools/archtest/scoperules"
)

// ResolveScope normalizes and validates a requested Scope. The empty value and
// the explicit "workspace" both map to ScopeWorkspace (back-compat default);
// "framework" maps to ScopeFramework; anything else is rejected fail-closed so a
// typo'd scope never silently runs the full suite (or nothing). It is the single
// scope-validation entry, called eagerly by the CLI (gocell verify archtest
// --scope) and again at the start of Run / ListTests so the Go API is also
// fail-closed.
func ResolveScope(s Scope) (Scope, error) {
	switch s {
	case "", ScopeWorkspace:
		return ScopeWorkspace, nil
	case ScopeFramework:
		return ScopeFramework, nil
	default:
		return "", fmt.Errorf(
			"archtestrunner: unknown scope %q (expected %q or %q)", s, ScopeWorkspace, ScopeFramework,
		)
	}
}

// frameworkTestFuncs resolves the framework-scope rule IDs
// ([scoperules.FrameworkRuleIDs] — the single membership source shared with
// tools/archtest.StandardCellRules) to the set of archtest test functions that
// assert them. It uses the FUNC-LEVEL index ([buildFrameworkFuncIndex]), not the
// file-level [buildRuleIndex] that backs --rule: in a theme-consolidated file a
// portable rule is anchored beside many gocell-internal rules, and only the
// portable rule's own section funcs belong in the framework scope.
func frameworkTestFuncs(workspaceRoot string) ([]string, error) {
	idx, err := buildFrameworkFuncIndex(workspaceRoot)
	if err != nil {
		return nil, err
	}
	return ruleFuncsForIDs(idx, scoperules.FrameworkRuleIDs)
}

// ruleFuncsForIDs returns the de-duplicated union of the test functions that
// assert each ID in ids. An ID that resolves to ZERO test functions (no
// INVARIANT anchor found) is a fail-loud integrity error naming the orphan: a
// framework rule that silently selects no test would be a false-green
// (the rule would not actually run under --scope=framework). Result order is
// stable (sorted) for deterministic sharding downstream.
func ruleFuncsForIDs(idx ruleIndex, ids []string) ([]string, error) {
	seen := make(map[string]bool)
	var out []string
	for _, id := range ids {
		funcs := idx[id]
		if len(funcs) == 0 {
			return nil, fmt.Errorf(
				"archtestrunner: framework rule %q has no test function"+
					" (no `// INVARIANT: %s` anchor found in %s/*_test.go);"+
					" the framework scope set (tools/archtest/scoperules.FrameworkRuleIDs)"+
					" and the archtest anchors are out of sync",
				id, id, archtestPkgDir,
			)
		}
		for _, fn := range funcs {
			if !seen[fn] {
				seen[fn] = true
				out = append(out, fn)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// selectByScope returns the subset of discovered that is in the allow set,
// preserving discovery order. It is the scope analog of the shard/rule/changed
// filters: a coarse narrowing applied before them.
func selectByScope(discovered, allow []string) []string {
	allowSet := make(map[string]bool, len(allow))
	for _, n := range allow {
		allowSet[n] = true
	}
	var out []string
	for _, n := range discovered {
		if allowSet[n] {
			out = append(out, n)
		}
	}
	return out
}

// applyScope narrows discovered to the requested scope. ScopeFramework keeps only
// the framework-portable rules' test functions; ScopeWorkspace (the normalized
// default) is a pass-through. Under --scope=framework, a --rule naming a
// workspace-only rule is a fail-loud error rather than a silent empty selection
// (which would read as a vacuous-green "framework run passed").
func applyScope(req Request, discovered []string) ([]string, error) {
	if req.Scope != ScopeFramework {
		return discovered, nil
	}
	if req.Rule != "" && !isFrameworkRule(req.Rule) {
		return nil, fmt.Errorf(
			"archtestrunner: rule %q is not in framework scope (it is a workspace-only rule);"+
				" drop --scope=framework or use --scope=workspace to run it", req.Rule,
		)
	}
	frameworkFuncs, err := frameworkTestFuncs(req.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	return selectByScope(discovered, frameworkFuncs), nil
}

// isFrameworkRule reports whether ruleID is part of the framework-scope set.
func isFrameworkRule(ruleID string) bool {
	for _, id := range scoperules.FrameworkRuleIDs {
		if id == ruleID {
			return true
		}
	}
	return false
}
