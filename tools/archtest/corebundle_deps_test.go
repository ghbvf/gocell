package archtest

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCorebundleDoesNotDependOnToolsDepgraph(t *testing.T) {
	root := findModuleRoot(t)
	module := readModulePath(t, root)

	cmd := exec.Command("go", "list", "-deps", "./cmd/corebundle") //nolint:gosec // const binary and args
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	for _, dep := range strings.Fields(string(output)) {
		assert.NotEqual(t, module+"/tools/depgraph", dep,
			"cmd/corebundle must not depend on tools/depgraph")
		assert.False(t, strings.HasPrefix(dep, "golang.org/x/tools"),
			"cmd/corebundle must not depend on golang.org/x/tools; found %s", dep)
	}
}
