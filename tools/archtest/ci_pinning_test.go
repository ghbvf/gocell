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

func TestGolangCILintVersionPinnedToPatch(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "_build-lint.yml"))
	require.NoError(t, err)

	re := regexp.MustCompile(`(?m)^\s*version:\s*(v[0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$`)
	matches := re.FindStringSubmatch(string(body))
	require.Len(t, matches, 2, "golangci-lint action version input must be present")
	assert.Regexp(t, regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`), matches[1],
		"golangci-lint must be pinned to patch version, not only major.minor")
}

func TestDependabotCoversCIAndGolangCILint(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "dependabot.yml"))
	require.NoError(t, err, ".github/dependabot.yml must exist")

	content := string(body)
	assert.Contains(t, content, `package-ecosystem: "github-actions"`,
		"dependabot must update GitHub Actions pins")
	assert.Contains(t, content, `package-ecosystem: "gomod"`,
		"dependabot must update Go module pins")
	assert.True(t,
		strings.Contains(content, "golangci/golangci-lint-action") ||
			strings.Contains(content, "golangci-lint"),
		"dependabot config must explicitly cover golangci-lint maintenance")
}
