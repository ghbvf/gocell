package assembly

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/testutil/fileutil"
)

// updateGolden regenerates this package's byte goldens when set (-update). It is the
// package-level golden-regeneration flag for package assembly; any new golden test in
// this package should reference this var rather than declare a second "-update" flag
// (a duplicate flag registration would panic at test startup).
var updateGolden = flag.Bool("update", false, "regenerate golden files")

// TestGenerateEntrypoint_FrameworkImportModuleIndependent is the Hard golden guard
// for issue #2126 (assembly side): the generated main.go's framework runtime imports
// must be the fixed gocell framework module path (github.com/ghbvf/gocell/framework/...),
// INDEPENDENT of the consumer's own module path.
//
// It renders under an EXTERNAL consumer module (github.com/acme/app) — the case the
// in-repo examples/*/main.go (module resolved to the org root github.com/ghbvf/gocell)
// could not expose, because there {{.Module}}/framework coincidentally equals the real
// framework path. Re-parameterizing the import by {{.Module}} would render
// github.com/acme/app/framework/... (a path that does not exist under the consumer
// module → uncompilable) and diverge from this byte golden → CI red.
func TestGenerateEntrypoint_FrameworkImportModuleIndependent(t *testing.T) {
	project := buildTestProject()
	project.Assemblies["demo"] = &metadata.AssemblyMeta{
		ID:    "demo",
		Cells: []metadata.AssemblyCellRef{{ID: "democell"}},
		Build: metadata.BuildMeta{Entrypoint: "cmd/demo/main.go", Binary: "demo", DeployTemplate: "k8s"},
	}
	// External consumer module — the framework import must NOT pick it up.
	gen := NewGenerator(project, "github.com/acme/app", "")

	out, err := gen.GenerateEntrypoint("demo")
	require.NoError(t, err)

	goldenPath := filepath.Join("testdata", "main_external_module.go.golden")
	if *updateGolden {
		require.NoError(t, os.WriteFile(goldenPath, out, 0o644))
		t.Logf("golden updated: %s", goldenPath)
		return
	}
	golden := fileutil.MustReadFile(t, goldenPath)
	if !bytes.Equal(out, golden) {
		t.Errorf("generated main.go diverges from golden:\n--- got ---\n%s\n--- want ---\n%s", out, golden)
	}
	// Belt: framework import must be the fixed gocell path AND must NOT carry the
	// consumer module path (issue #2126) — grouping-independent semantic checks that
	// complement the byte golden (so a carelessly emptied golden cannot pass silently).
	if !bytes.Contains(out, []byte("github.com/ghbvf/gocell/framework/runtime/shutdown")) {
		t.Errorf("generated main.go missing the fixed framework import (issue #2126):\n%s", out)
	}
	if bytes.Contains(out, []byte("github.com/acme/app/framework")) {
		t.Errorf("framework import leaked consumer module path (issue #2126):\n%s", out)
	}
}
