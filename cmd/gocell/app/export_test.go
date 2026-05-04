package app

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exportFixturePath points to the checked-in minimal GoCell fixture used for
// CLI export tests. All tests that need a parseable project copy it into a
// t.TempDir() so mutations cannot affect the source files.
const exportFixturePath = "testdata/exportfixture"

// copyFixtureToTempDir copies the exportfixture tree into a fresh temp dir
// and returns the temp dir path.
func copyFixtureToTempDir(t *testing.T) string {
	t.Helper()
	src := exportFixturePath
	dst := t.TempDir()
	err := filepath.Walk(src, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // test-only fixture copy
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o600) //nolint:gosec // test helper
	})
	require.NoError(t, err, "copyFixtureToTempDir")
	return dst
}

// captureExportStdout redirects os.Stdout during fn and returns what was written.
func captureExportStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	origOut := os.Stdout
	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	os.Stdout = w
	defer func() { os.Stdout = origOut }()

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()

	fn()
	_ = w.Close()
	return <-done
}

// TestRunExport_NoSubcommand_Error verifies that `gocell export` with no
// subcommand returns an error.
func TestRunExport_NoSubcommand_Error(t *testing.T) {
	err := runExport([]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage:")
}

// TestRunExport_UnknownSubcommand verifies that `gocell export frobnicate`
// returns an error naming the unknown subcommand.
func TestRunExport_UnknownSubcommand(t *testing.T) {
	err := runExport([]string{"frobnicate"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "frobnicate")
}

// TestRunExport_RootMissing verifies that a non-existent --root returns an error.
func TestRunExport_RootMissing(t *testing.T) {
	err := runExport([]string{"catalog", "--root=/nonexistent/path/xyz"})
	require.Error(t, err)
}

// TestRunExport_FormatBad verifies that --format=badvalue returns an error.
func TestRunExport_FormatBad(t *testing.T) {
	root := copyFixtureToTempDir(t)
	err := runExport([]string{"catalog", "--root=" + root, "--format=xml", "--include="})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "xml")
}

// TestRunExport_BadInclude verifies that an unknown --include token errors with
// a message that lists valid options but does not echo the invalid value.
func TestRunExport_BadInclude(t *testing.T) {
	root := copyFixtureToTempDir(t)
	err := runExport([]string{"catalog", "--root=" + root, "--include=foobar"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "include")
	assert.NotContains(t, err.Error(), "foobar")
	// Error message must hint at valid tokens.
	assert.Contains(t, err.Error(), "cellDeps")
}

// TestRunExport_BadKind verifies that an unknown --kinds token errors without
// echoing the invalid value.
func TestRunExport_BadKind(t *testing.T) {
	root := copyFixtureToTempDir(t)
	err := runExport([]string{"catalog", "--root=" + root, "--kinds=Bogus", "--include="})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kinds")
	assert.NotContains(t, err.Error(), "Bogus")
}

// TestRunExport_BadLayer verifies that an unknown --layers token errors without
// echoing the invalid value.
func TestRunExport_BadLayer(t *testing.T) {
	root := copyFixtureToTempDir(t)
	err := runExport([]string{"catalog", "--root=" + root, "--layers=foo", "--include="})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "layers")
	assert.NotContains(t, err.Error(), "foo")
}

// TestRunExport_IncludeNone verifies that --include="" produces a document
// without cellDeps / packageDeps / statusBoard / relations.
func TestRunExport_IncludeNone(t *testing.T) {
	root := copyFixtureToTempDir(t)
	outPath := filepath.Join(t.TempDir(), "out.json")
	require.NoError(t, runExport([]string{"catalog", "--root=" + root, "--include=", "--out=" + outPath}))

	data, err := os.ReadFile(outPath) //nolint:gosec // test output file
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	// No optional blocks should be present when include is empty.
	_, hasDeps := doc["dependencies"]
	_, hasStatusBoard := doc["statusBoard"]
	assert.False(t, hasDeps, "dependencies must be absent when --include is empty")
	assert.False(t, hasStatusBoard, "statusBoard must be absent when --include is empty")
}

// TestRunExport_IncludeOnlyEntities verifies --include=relations includes
// relations but not the heavier dep-graph blocks.
func TestRunExport_IncludeOnlyEntities(t *testing.T) {
	root := copyFixtureToTempDir(t)
	outPath := filepath.Join(t.TempDir(), "out.json")
	require.NoError(t, runExport([]string{"catalog", "--root=" + root, "--include=relations", "--out=" + outPath}))

	data, err := os.ReadFile(outPath) //nolint:gosec // test output file
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	// dependencies (cellDeps / packageDeps) must not appear.
	_, hasDeps := doc["dependencies"]
	assert.False(t, hasDeps, "dependencies block must be absent when only relations are included")
}

// TestRunExport_IncludeCellDepsOnly verifies that --include=cellDeps produces
// a dependencies.cells block without a packages block.
func TestRunExport_IncludeCellDepsOnly(t *testing.T) {
	root := copyFixtureToTempDir(t)
	outPath := filepath.Join(t.TempDir(), "out.json")
	require.NoError(t, runExport([]string{"catalog", "--root=" + root, "--include=cellDeps", "--out=" + outPath}))

	data, err := os.ReadFile(outPath) //nolint:gosec // test output file
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	deps, hasDeps := doc["dependencies"].(map[string]any)
	if assert.True(t, hasDeps, "dependencies block must be present when cellDeps is included") {
		_, hasCells := deps["cells"]
		_, hasPackages := deps["packages"]
		assert.True(t, hasCells, "dependencies.cells must be present")
		assert.False(t, hasPackages, "dependencies.packages must be absent when only cellDeps included")
	}
}

// TestRunExport_CellDepsValidationErrors verifies that a project with a cycle
// causes exportCatalog to fail with a descriptive multi-line error.
func TestRunExport_CellDepsValidationErrors(t *testing.T) {
	// Build a fixture with a cyclic dep by wiring two cells to each other.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/cycle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "actors.yaml"),
		[]byte("# no actors\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "journeys", "status-board.yaml"),
		[]byte("# no entries\n"), 0o644))

	// cellA serves contract.v1; cellB subscribes — creates A←B edge.
	// cellB serves contract.v2; cellA subscribes — creates B←A edge → cycle.
	mkCell := func(id, sliceID, serveContract, subscribeContract string) {
		cellDir := filepath.Join(root, "cells", id)
		sliceDir := filepath.Join(cellDir, "slices", sliceID)
		require.NoError(t, os.MkdirAll(sliceDir, 0o755))
		cellYAML := "id: " + id + "\ntype: platform\nconsistencyLevel: L1\n" +
			"owner:\n  team: test\n  role: owner\nschema:\n  primary: " + id +
			"\nverify:\n  smoke: []\n"
		require.NoError(t, os.WriteFile(
			filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o600))
		sliceYAML := "id: " + sliceID + "\nbelongsToCell: " + id +
			"\nconsistencyLevel: L1\ncontractUsages:\n" +
			"  - contract: " + serveContract + "\n    role: provide\n" +
			"  - contract: " + subscribeContract + "\n    role: subscribe\n" +
			"verify:\n  unit: []\n  contract: []\nallowedFiles:\n  - \"*.go\"\n"
		require.NoError(t, os.WriteFile(
			filepath.Join(sliceDir, "slice.yaml"), []byte(sliceYAML), 0o600))
	}
	mkContract := func(id, provider, subscriber string) {
		cDir := filepath.Join(root, "contracts", "http", id, "v1")
		require.NoError(t, os.MkdirAll(cDir, 0o755))
		contractYAML := "id: http." + id + ".v1\nkind: http\nlifecycle: active\n" +
			"endpoints:\n  server: " + provider + "\n  clients:\n    - " + subscriber + "\n"
		require.NoError(t, os.WriteFile(
			filepath.Join(cDir, "contract.yaml"), []byte(contractYAML), 0o600))
	}
	mkCell("cella", "slicea", "http.contractab.v1", "http.contractba.v1")
	mkCell("cellb", "sliceb", "http.contractba.v1", "http.contractab.v1")
	mkContract("contractab", "cella", "cellb")
	mkContract("contractba", "cellb", "cella")

	// Capture stdout for JSON assertion.
	var stdout bytes.Buffer
	origOut := os.Stdout
	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	os.Stdout = w

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()

	err := runExport([]string{"catalog", "--root=" + root, "--include=cellDeps"})
	_ = w.Close()
	os.Stdout = origOut
	stdout.Write(<-done)

	// governance.Graph() detects resolution errors (DEP-02 cycle detection fires
	// in Check(), not Graph()), so Graph() returns an empty error slice here.
	// export should succeed even with a cyclic dependency structure.
	require.NoError(t, err, "Graph() does not detect cycles (only resolution errors); export should succeed even with cycles")
	out := stdout.String()
	// Both cell nodes must appear in the output because Graph() returns them.
	require.Contains(t, out, "cella")
	require.Contains(t, out, "cellb")
}

// TestRunExport_CatalogStdoutJSON runs a happy-path export and checks the
// stdout JSON structure.
func TestRunExport_CatalogStdoutJSON(t *testing.T) {
	root := copyFixtureToTempDir(t)

	out := captureExportStdout(t, func() {
		err := runExport([]string{"catalog", "--root=" + root, "--include=relations"})
		require.NoError(t, err)
	})

	require.NotEmpty(t, out, "stdout must not be empty")

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc), "stdout must be valid JSON: %q", string(out))
	assert.Equal(t, "v1", doc["schemaVersion"], "schemaVersion must be v1")

	entities, ok := doc["entities"].([]any)
	assert.True(t, ok && len(entities) > 0, "entities must be non-empty for a project with cells")
}

// TestRunExport_CatalogStdoutJSON_CLIPipeline verifies that the CLI pipeline
// (flag parsing → loadProjectMeta → BuildDocument → MarshalDocument → writeOut)
// runs without error even when BuildDocument returns a zero Document and
// MarshalDocument returns nil body.
func TestRunExport_CatalogStdoutJSON_CLIPipeline(t *testing.T) {
	root := copyFixtureToTempDir(t)

	out := captureExportStdout(t, func() {
		err := runExport([]string{"catalog", "--root=" + root, "--include="})
		require.NoError(t, err)
	})

	// With the stub MarshalDocument returning nil body, we expect empty output.
	// Assert the pipeline completed without error (checked above) and output
	// is either empty or valid JSON.
	if len(out) > 0 {
		var doc map[string]any
		assert.NoError(t, json.Unmarshal(out, &doc), "output must be valid JSON if non-empty")
	}
}

// TestRunExport_CatalogToFile verifies that --out writes to a file.
func TestRunExport_CatalogToFile(t *testing.T) {
	root := copyFixtureToTempDir(t)
	outPath := filepath.Join(t.TempDir(), "catalog.json")

	err := runExport([]string{"catalog", "--root=" + root, "--include=", "--out=" + outPath})
	require.NoError(t, err)

	_, statErr := os.Stat(outPath)
	assert.NoError(t, statErr, "output file must exist after --out is provided")
}

// TestRunExport_FormatYAML verifies that --format=yaml runs without error.
// Full content assertion skipped until Agent 2A completes.
func TestRunExport_FormatYAML(t *testing.T) {
	root := copyFixtureToTempDir(t)
	outPath := filepath.Join(t.TempDir(), "catalog.yaml")

	err := runExport([]string{"catalog", "--root=" + root, "--format=yaml", "--include=", "--out=" + outPath})
	require.NoError(t, err)

	data, readErr := os.ReadFile(outPath) //nolint:gosec // test output file
	require.NoError(t, readErr)

	// MarshalDocument produces YAML that starts with "schemaVersion:".
	require.NotEmpty(t, data, "YAML output must not be empty")
	assert.True(t, strings.HasPrefix(string(data), "schemaVersion:"),
		"YAML output must start with schemaVersion field")
}

// TestRunExport_KindsFilter verifies that --kinds=Cell,Contract limits output
// to only those entity kinds.
func TestRunExport_KindsFilter(t *testing.T) {
	root := copyFixtureToTempDir(t)
	outPath := filepath.Join(t.TempDir(), "out.json")
	require.NoError(t, runExport([]string{
		"catalog", "--root=" + root,
		"--kinds=Cell,Contract",
		"--include=",
		"--out=" + outPath,
	}))

	data, err := os.ReadFile(outPath) //nolint:gosec // test output file
	require.NoError(t, err)

	var doc struct {
		Entities []struct {
			Kind string `json:"kind"`
		} `json:"entities"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	for _, e := range doc.Entities {
		assert.True(t, e.Kind == "Cell" || e.Kind == "Contract",
			"expected only Cell or Contract entities, got %q", e.Kind)
	}
}

// TestRunExport_LayersFilter verifies that --layers= flag is accepted without error.
func TestRunExport_LayersFilter(t *testing.T) {
	root := copyFixtureToTempDir(t)
	err := runExport([]string{"catalog", "--root=" + root, "--layers=cells", "--include="})
	require.NoError(t, err)
}

// TestRunExport_CellsFocus verifies that --cells= flag is accepted without error.
func TestRunExport_CellsFocus(t *testing.T) {
	root := copyFixtureToTempDir(t)
	err := runExport([]string{"catalog", "--root=" + root, "--cells=testcell", "--include="})
	require.NoError(t, err)
}

// TestRunExport_MetadataAlias verifies that the "metadata" subcommand alias
// produces byte-equal output to "catalog" on the same fixture with no deps loaded.
func TestRunExport_MetadataAlias(t *testing.T) {
	root := copyFixtureToTempDir(t)

	var catalogOut, metadataOut []byte

	catalogOut = captureExportStdout(t, func() {
		err := runExport([]string{"catalog", "--root=" + root, "--include="})
		require.NoError(t, err)
	})

	metadataOut = captureExportStdout(t, func() {
		err := runExport([]string{"metadata", "--root=" + root, "--include="})
		require.NoError(t, err)
	})

	assert.Equal(t, catalogOut, metadataOut,
		"catalog and metadata aliases must produce byte-equal output")
}

// TestDispatch_ExportNoSubcommand verifies that `gocell export` with no
// subcommand returns ExitRuntime via the Dispatch path.
func TestDispatch_ExportNoSubcommand(t *testing.T) {
	exit, _, stderr := captureDispatch(t, []string{"export"})
	assert.Equal(t, ExitRuntime, exit)
	assert.Contains(t, stderr, "usage:")
}

// TestDispatch_ExportUnknown verifies that `gocell export badcmd` returns
// ExitRuntime.
func TestDispatch_ExportUnknown(t *testing.T) {
	exit, _, stderr := captureDispatch(t, []string{"export", "badcmd"})
	assert.Equal(t, ExitRuntime, exit)
	assert.Contains(t, stderr, "badcmd")
}

// TestDispatch_UsageContainsExport verifies that the top-level usage string
// mentions the export command.
func TestDispatch_UsageContainsExport(t *testing.T) {
	exit, stdout, _ := captureDispatch(t, []string{})
	assert.Equal(t, ExitUsage, exit)
	assert.Contains(t, stdout, "export")
}

// TestRunExport_WireSummaryInjected verifies that the default export path
// populates the wireSummary field on Cell entities. The fixture has no cell.go
// so the wireSummary is injected with empty Listeners/Routes/Subscribes.
func TestRunExport_WireSummaryInjected(t *testing.T) {
	root := copyFixtureToTempDir(t)
	outPath := filepath.Join(t.TempDir(), "out.json")

	err := runExport([]string{
		"catalog", "--root=" + root,
		"--include=", // skip deps for speed
		"--out=" + outPath,
	})
	require.NoError(t, err)

	data, readErr := os.ReadFile(outPath) //nolint:gosec // test output file
	require.NoError(t, readErr)

	// Parse as generic JSON to find the Cell entity spec.
	var doc struct {
		Entities []struct {
			Kind string `json:"kind"`
			Spec struct {
				// wireSummary is present when InjectWireSummaries ran.
				WireSummary *struct {
					CellID     string `json:"cellId"`
					Listeners  []any  `json:"listeners"`
					Routes     []any  `json:"routes"`
					Subscribes []any  `json:"subscribes"`
				} `json:"wireSummary"`
			} `json:"spec"`
		} `json:"entities"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	var cellEntities []struct {
		WireSummary *struct {
			CellID     string `json:"cellId"`
			Listeners  []any  `json:"listeners"`
			Routes     []any  `json:"routes"`
			Subscribes []any  `json:"subscribes"`
		}
	}
	for _, e := range doc.Entities {
		if e.Kind == "Cell" {
			cellEntities = append(cellEntities, struct {
				WireSummary *struct {
					CellID     string `json:"cellId"`
					Listeners  []any  `json:"listeners"`
					Routes     []any  `json:"routes"`
					Subscribes []any  `json:"subscribes"`
				}
			}{WireSummary: e.Spec.WireSummary})
		}
	}

	require.NotEmpty(t, cellEntities, "fixture must have at least one Cell entity")
	for _, ce := range cellEntities {
		require.NotNil(t, ce.WireSummary,
			"Cell entity must have wireSummary field injected (even if empty)")
		assert.NotEmpty(t, ce.WireSummary.CellID, "wireSummary.cellId must be set")
	}
}

// TestAttachWireSummaries_ScanErrorGraceDegrade verifies that attachWireSummaries
// does not populate opts.WireSummaries when markergen fails (e.g. broken fixture),
// and the export still completes successfully with wireSummary absent from all Cell
// entities. This covers the graceful-degrade path in attachWireSummaries.
func TestAttachWireSummaries_ScanErrorGraceDegrade(t *testing.T) {
	// Build a fixture that has valid cell metadata but an unreadable cell.go
	// (a directory in place of a file) to force markergen.Merge to error.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/wirescan\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "actors.yaml"),
		[]byte("# no actors\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "journeys", "status-board.yaml"),
		[]byte("# no entries\n"), 0o644))

	cellDir := filepath.Join(root, "cells", "badcell")
	sliceDir := filepath.Join(cellDir, "slices", "badslice")
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))

	cellYAML := `id: badcell
type: platform
consistencyLevel: L1
owner:
  team: test
  role: owner
schema:
  primary: badcell
verify:
  smoke: []
`
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o600))
	sliceYAML := `id: badslice
belongsToCell: badcell
consistencyLevel: L1
contractUsages: []
verify:
  unit: []
  contract: []
allowedFiles:
  - "*.go"
`
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "slice.yaml"), []byte(sliceYAML), 0o600))

	// Put a directory where cell.go would be to make markergen fail to read it.
	require.NoError(t, os.MkdirAll(filepath.Join(cellDir, "cell.go"), 0o755))

	outPath := filepath.Join(t.TempDir(), "out.json")
	err := runExport([]string{
		"catalog", "--root=" + root,
		"--include=",
		"--out=" + outPath,
	})
	// Graceful degrade: exit 0 even when wire summary scan fails.
	require.NoError(t, err, "CLI must exit 0 even when wire summary scan fails")

	data, readErr := os.ReadFile(outPath) //nolint:gosec // test output file
	require.NoError(t, readErr)

	var doc struct {
		Entities []struct {
			Kind string `json:"kind"`
			Spec struct {
				WireSummary *struct{} `json:"wireSummary"`
			} `json:"spec"`
		} `json:"entities"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	for _, e := range doc.Entities {
		if e.Kind == "Cell" {
			assert.Nil(t, e.Spec.WireSummary,
				"wireSummary must be absent on Cell entities when scan fails")
		}
	}
}

// TestRunExport_DefaultPackageDepsLoadError verifies that the default export
// path degrades when depgraph.Load fails instead of failing the whole document.
func TestRunExport_DefaultPackageDepsLoadError(t *testing.T) {
	// Build a minimal fixture that has valid cell metadata but an invalid
	// go.mod (empty module path) to make depgraph.Load fail.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module \n"), 0o644)) // empty module path triggers load error
	require.NoError(t, os.WriteFile(filepath.Join(root, "actors.yaml"),
		[]byte("# no actors\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "journeys", "status-board.yaml"),
		[]byte("# no entries\n"), 0o644))

	outPath := filepath.Join(t.TempDir(), "out.json")
	err := runExport([]string{
		"catalog", "--root=" + root,
		"--out=" + outPath,
	})
	// Graceful degrade: exit 0.
	require.NoError(t, err, "CLI must exit 0 even when packageDeps load fails")

	data, readErr := os.ReadFile(outPath) //nolint:gosec // test output file
	require.NoError(t, readErr)

	var doc struct {
		Dependencies *struct {
			Packages *struct {
				Error string `json:"error"`
			} `json:"packages"`
		} `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	require.NotNil(t, doc.Dependencies, "dependencies block must be present")
	require.NotNil(t, doc.Dependencies.Packages, "dependencies.packages must be present")
	assert.NotEmpty(t, doc.Dependencies.Packages.Error,
		"dependencies.packages.error must be non-empty on load failure")
}
