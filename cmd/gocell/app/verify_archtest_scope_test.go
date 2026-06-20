package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cmd/gocell/internal/archtestrunner"
)

// TestParseArchtestFlags_Scope: --scope parses into Request.Scope; empty defaults
// to workspace (back-compat); unknown values are rejected at parse time.
func TestParseArchtestFlags_Scope(t *testing.T) {
	root := t.TempDir() // avoid findRoot() dependency; parse does not stat the root

	valid := []struct {
		name string
		args []string
		want archtestrunner.Scope
	}{
		{name: "default", args: []string{"--root=" + root}, want: archtestrunner.ScopeWorkspace},
		{name: "explicit workspace", args: []string{"--scope=workspace", "--root=" + root}, want: archtestrunner.ScopeWorkspace},
		{name: "framework", args: []string{"--scope=framework", "--root=" + root}, want: archtestrunner.ScopeFramework},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			req, _, _, err := parseArchtestFlags(tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.want, req.Scope)
		})
	}

	t.Run("unknown scope rejected", func(t *testing.T) {
		_, _, _, err := parseArchtestFlags([]string{"--scope=bogus", "--root=" + root})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bogus", "error must name the bad scope value")
		assert.Contains(t, err.Error(), "scope")
	})
}

// TestVerifyArchtest_BadScope: an unknown --scope fails the whole dispatch with
// ExitRuntime before any archtest run (mirrors TestVerifyArchtest_BadShard).
func TestVerifyArchtest_BadScope(t *testing.T) {
	ctx := context.Background()
	exit, _, stderr := captureDispatch(t, ctx, []string{"verify", "archtest", "--scope=bogus"})
	assert.Equal(t, ExitRuntime, exit, "bad scope must exit 1")
	assert.Contains(t, stderr, "scope", "stderr must mention the scope flag")
}

// TestVerifyArchtest_HelpMentionsScope: the archtest flag usage advertises
// --scope so the option is discoverable.
func TestVerifyArchtest_HelpMentionsScope(t *testing.T) {
	ctx := context.Background()
	_, stdout, stderr := captureDispatch(t, ctx, []string{"verify", "archtest", "-h"})
	assert.Contains(t, stdout+stderr, "scope", "archtest -h must advertise the --scope flag")
}
