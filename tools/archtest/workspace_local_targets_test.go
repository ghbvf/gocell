// INVARIANT: WORKSPACE-LOCAL-TARGETS-01
//
// WORKSPACE-LOCAL-TARGETS-01 - local root targets that are expected to cover
// the whole go.work workspace must traverse hack/lib/modules.sh instead of
// root-only `./...`, because nested modules such as ./tools are otherwise
// silently skipped by formatter, verify, and coverage gates.
package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceLocalTargetsUseModuleFunnel(t *testing.T) {
	root := findModuleRoot(t)
	makefile := readTextFile(t, filepath.Join(root, "Makefile"))
	verifyGofumpt := readTextFile(t, filepath.Join(root, "hack", "verify-gofumpt.sh"))

	for _, target := range []string{"fmt", "cover"} {
		body := makeTargetBody(t, makefile, target)
		funnelText := makeTargetFunnelText(t, root, body)
		assert.Contains(t, funnelText, "hack/lib/modules.sh", "make %s must source the workspace module funnel", target)
		assert.Regexp(t, regexp.MustCompile(`(?m)^[^#\n]*gocell::modules::dirs`), funnelText,
			"make %s must invoke gocell::modules::dirs on a non-comment line", target)
	}

	assert.Contains(t, verifyGofumpt, "hack/lib/modules.sh",
		"verify-gofumpt must source the workspace module funnel")
	assert.Regexp(t, regexp.MustCompile(`(?m)^[^#\n]*gocell::modules::dirs`), verifyGofumpt,
		"verify-gofumpt must invoke gocell::modules::dirs on a non-comment line")
}

func makeTargetBody(t *testing.T, makefile, target string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `:\n`)
	loc := re.FindStringIndex(makefile)
	require.NotNil(t, loc, "Makefile target %q not found", target)
	rest := makefile[loc[1]:]
	next := regexp.MustCompile(`(?m)^[A-Za-z0-9_.-]+:\n`).FindStringIndex(rest)
	if next == nil {
		return rest
	}
	return rest[:next[0]]
}

func makeTargetFunnelText(t *testing.T, root, body string) string {
	t.Helper()
	if regexp.MustCompile(`(?m)^[^#\n]*\bhack/cover-workspace\.sh\b`).MatchString(body) {
		return body + "\n" + readTextFile(t, filepath.Join(root, "hack", "cover-workspace.sh"))
	}
	return body
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // path is findModuleRoot-derived and fixed by this archtest.
	require.NoError(t, err)
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}
