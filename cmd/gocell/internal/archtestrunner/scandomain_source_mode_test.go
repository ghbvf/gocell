package archtestrunner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourceModeFixture is a set of archtest rule files with distinct scopes,
// used to exercise --changed source-mode selection.
func sourceModeFixture() map[string]string {
	const hdr = "//go:build archtest\n\npackage archtest\n\nimport \"testing\"\n\n"
	return map[string]string{
		"redis_test.go":     "//go:build archtest\n\n// INVARIANT: REDIS-01\n\npackage archtest\n\nimport \"testing\"\n\nfunc TestRedis(t *testing.T) { Run(t, Typed(TypedOpts{}, []string{\"./adapters/redis/...\"}), nil) }\n",
		"framework_test.go": "//go:build archtest\n\n// INVARIANT: FW-01\n\npackage archtest\n\nimport \"testing\"\n\nfunc TestFw(t *testing.T) { Run(t, Typed(TypedOpts{}, []string{\"./framework/kernel/...\"}), nil) }\n",
		"prod_test.go":      "//go:build archtest\n\n// INVARIANT: PROD-01\n\npackage archtest\n\nimport \"testing\"\n\nfunc TestProd(t *testing.T) { Run(t, Production(TypedOpts{Tests: false}), nil) }\n",
		"unknown_test.go":   hdr + "// INVARIANT: UNK-01\nfunc TestUnk(t *testing.T) { pkgs := loadPkgs(); Run(t, Typed(TypedOpts{}, pkgs), nil) }\n",
	}
}

func sourceModeEngine(changed func(context.Context, string) ([]string, error)) engine {
	return engine{
		exec: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			return nil, nil // applyFilters never execs
		},
		changed: changed,
	}
}

// TestApplyFilters_SourceMode_NarrowGoChange verifies that a change confined to
// adapters/redis selects the redis-scoped rule + the Production-floor rule +
// the unknown rule, but NOT a rule scoped to a different area.
func TestApplyFilters_SourceMode_NarrowGoChange(t *testing.T) {
	root := makeFakeArchtestDir(t, sourceModeFixture())
	discovered := []string{"TestRedis", "TestFw", "TestProd", "TestUnk"}
	e := sourceModeEngine(func(_ context.Context, _ string) ([]string, error) {
		return []string{"adapters/redis/client.go"}, nil
	})

	got, err := e.applyFilters(context.Background(), Request{WorkspaceRoot: root, Changed: true}, discovered)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"TestRedis", "TestProd", "TestUnk"}, got,
		"redis-scoped + Production-floor + unknown selected; framework-scoped excluded")
}

// TestApplyFilters_SourceMode_DocOnlyChange verifies that a docs-only change
// skips every Go-scanning rule and selects only the unknown (always-run) rule.
func TestApplyFilters_SourceMode_DocOnlyChange(t *testing.T) {
	root := makeFakeArchtestDir(t, sourceModeFixture())
	discovered := []string{"TestRedis", "TestFw", "TestProd", "TestUnk"}
	e := sourceModeEngine(func(_ context.Context, _ string) ([]string, error) {
		return []string{"docs/readme.md"}, nil
	})

	got, err := e.applyFilters(context.Background(), Request{WorkspaceRoot: root, Changed: true}, discovered)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"TestUnk"}, got,
		"a docs-only change selects only unknown-scope rules")
}

// TestApplyFilters_SourceMode_RuleFileEdited verifies mechanical subsumption:
// editing an archtest rule file still selects that file's own tests, and a rule
// scoped to an unrelated area is still excluded.
func TestApplyFilters_SourceMode_RuleFileEdited(t *testing.T) {
	root := makeFakeArchtestDir(t, sourceModeFixture())
	discovered := []string{"TestRedis", "TestFw", "TestProd", "TestUnk"}
	e := sourceModeEngine(func(_ context.Context, _ string) ([]string, error) {
		return []string{"tools/archtest/redis_test.go"}, nil
	})

	got, err := e.applyFilters(context.Background(), Request{WorkspaceRoot: root, Changed: true}, discovered)
	require.NoError(t, err)
	assert.Contains(t, got, "TestRedis", "editing a rule file must still select its own tests")
	assert.NotContains(t, got, "TestFw", "a rule scoped to an unrelated area is excluded")
}

// TestApplyFilters_SourceMode_EmptyChange verifies an empty changed-file set
// yields an empty selection (trivially passes upstream).
func TestApplyFilters_SourceMode_EmptyChange(t *testing.T) {
	root := makeFakeArchtestDir(t, sourceModeFixture())
	discovered := []string{"TestRedis", "TestFw", "TestProd", "TestUnk"}
	e := sourceModeEngine(func(_ context.Context, _ string) ([]string, error) {
		return nil, nil
	})

	got, err := e.applyFilters(context.Background(), Request{WorkspaceRoot: root, Changed: true}, discovered)
	require.NoError(t, err)
	assert.Empty(t, got, "no changed files => no rules selected")
}

// TestApplyFilters_SourceMode_ShardComposes verifies --shard applies before
// --changed source filtering (shard narrows the candidate set first).
func TestApplyFilters_SourceMode_ShardComposes(t *testing.T) {
	root := makeFakeArchtestDir(t, sourceModeFixture())
	// Sorted discovery: [TestFw, TestProd, TestRedis, TestUnk].
	// shard 0 of 2 keeps NR even (1-based): TestProd(2), TestUnk(4).
	discovered := []string{"TestFw", "TestProd", "TestRedis", "TestUnk"}
	e := sourceModeEngine(func(_ context.Context, _ string) ([]string, error) {
		return []string{"adapters/redis/client.go"}, nil
	})

	got, err := e.applyFilters(context.Background(),
		Request{WorkspaceRoot: root, Changed: true, Shard: Shard{Index: 0, Total: 2}}, discovered)
	require.NoError(t, err)
	// Of the shard {TestProd, TestUnk}: TestProd (productionGo) + TestUnk (unknown) match;
	// TestRedis is not in this shard even though it matches the change.
	assert.ElementsMatch(t, []string{"TestProd", "TestUnk"}, got)
}
