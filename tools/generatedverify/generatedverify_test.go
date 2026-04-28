package generatedverify

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixtureModule = "example.com/generatedfixture"

func TestExpectedArtifactsDerivesManifestFromMetadata(t *testing.T) {
	root, project := newGeneratedFixture(t)

	artifacts, err := ExpectedArtifacts(root, fixtureModule, project)
	require.NoError(t, err)

	require.Len(t, artifacts, 3)
	assert.Equal(t, []string{
		"cmd/fixture/main.go",
		"assemblies/fixture/generated/boundary.yaml",
		"assemblies/fixture/generated/metrics-schema.yaml",
	}, artifactPaths(artifacts))
	assert.Equal(t, []string{
		"assembly-entrypoint",
		"boundary",
		"metrics-schema",
	}, artifactKinds(artifacts))
	assert.Contains(t, string(artifacts[0].Content), "runFixture")
	assert.Contains(t, string(artifacts[1].Content), "assemblyId: fixture")
	assert.Contains(t, string(artifacts[2].Content), "entrypoint: cmd/fixture/main.go")
}

func TestVerifyPassesWhenExpectedFilesAreTracked(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)
	gitRun(t, root, "init")
	gitRun(t, root, "add", artifactPaths(artifacts)...)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.True(t, result.Passed())
	assert.Empty(t, result.Drifts)
	assert.Len(t, result.Artifacts, 3)
}

func TestVerifyReportsMissingAndChangedArtifacts(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts, err := ExpectedArtifacts(root, fixtureModule, project)
	require.NoError(t, err)

	staleEntrypoint := append([]byte(nil), artifacts[0].Content...)
	staleEntrypoint = append(staleEntrypoint, []byte("\n// stale generated main\n")...)
	writeFile(t, root, artifacts[0].Path, staleEntrypoint)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	assert.Equal(t, []Drift{
		{
			AssemblyID: "fixture",
			Kind:       "boundary",
			Path:       "assemblies/fixture/generated/boundary.yaml",
			Message:    "file is missing",
		},
		{
			AssemblyID: "fixture",
			Kind:       "metrics-schema",
			Path:       "assemblies/fixture/generated/metrics-schema.yaml",
			Message:    "file is missing",
		},
		{
			AssemblyID: "fixture",
			Kind:       "assembly-entrypoint",
			Path:       "cmd/fixture/main.go",
			Message:    "content differs",
		},
	}, result.Drifts)
}

func TestVerifyReportsUntrackedArtifacts(t *testing.T) {
	root, project := newGeneratedFixture(t)
	writeExpectedArtifacts(t, root, project)
	gitRun(t, root, "init")

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	assert.Equal(t, []Drift{
		{
			AssemblyID: "fixture",
			Kind:       "boundary",
			Path:       "assemblies/fixture/generated/boundary.yaml",
			Message:    "file is not tracked by git",
		},
		{
			AssemblyID: "fixture",
			Kind:       "metrics-schema",
			Path:       "assemblies/fixture/generated/metrics-schema.yaml",
			Message:    "file is not tracked by git",
		},
		{
			AssemblyID: "fixture",
			Kind:       "assembly-entrypoint",
			Path:       "cmd/fixture/main.go",
			Message:    "file is not tracked by git",
		},
	}, result.Drifts)
}

func TestExpectedArtifactsRejectsInvalidInputs(t *testing.T) {
	root := t.TempDir()

	_, err := ExpectedArtifacts(root, fixtureModule, nil)
	require.ErrorContains(t, err, "project metadata is nil")

	_, err = ExpectedArtifacts(root, "", &metadata.ProjectMeta{})
	require.ErrorContains(t, err, "module path is required")

	_, err = ExpectedArtifacts(root, fixtureModule, &metadata.ProjectMeta{
		Assemblies: map[string]*metadata.AssemblyMeta{"fixture": nil},
	})
	require.ErrorContains(t, err, `assembly "fixture" metadata is nil`)
}

func TestValidateArtifactPathsRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()

	err := validateArtifactPaths(root, []Artifact{{
		AssemblyID: "fixture",
		Kind:       "assembly-entrypoint",
		Path:       filepath.Join(root, "cmd/fixture/main.go"),
	}})
	require.ErrorContains(t, err, "must be repo-relative")

	err = validateArtifactPaths(root, []Artifact{{
		AssemblyID: "fixture",
		Kind:       "assembly-entrypoint",
		Path:       "../cmd/fixture/main.go",
	}})
	require.ErrorContains(t, err, "escapes project root")
}

func newGeneratedFixture(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()

	root := t.TempDir()
	writeFile(t, root, "go.mod", []byte("module "+fixtureModule+"\n\ngo 1.25.0\n"))
	writeFile(t, root, "runtime/shutdown/shutdown.go", []byte(`package shutdown

import "context"

func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}
`))
	writeFile(t, root, "cmd/fixture/run.go", []byte(`package main

import "context"

func runFixture(context.Context, string, []string) error {
	return nil
}
`))

	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	project.Assemblies["fixture"] = &metadata.AssemblyMeta{
		ID:    "fixture",
		Cells: []string{},
		Build: metadata.BuildMeta{
			Entrypoint: "cmd/fixture/main.go",
			Binary:     "bin/fixture",
		},
	}
	return root, project
}

func writeExpectedArtifacts(t *testing.T, root string, project *metadata.ProjectMeta) []Artifact {
	t.Helper()

	artifacts, err := ExpectedArtifacts(root, fixtureModule, project)
	require.NoError(t, err)
	for _, artifact := range artifacts {
		writeFile(t, root, artifact.Path, artifact.Content)
	}
	return artifacts
}

func writeFile(t *testing.T, root, rel string, content []byte) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, content, 0o644))
}

func artifactPaths(artifacts []Artifact) []string {
	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		paths = append(paths, artifact.Path)
	}
	return paths
}

func artifactKinds(artifacts []Artifact) []string {
	kinds := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		kinds = append(kinds, artifact.Kind)
	}
	return kinds
}

func gitRun(t *testing.T, root, name string, args ...string) {
	t.Helper()

	fullArgs := append([]string{"-C", root, name}, args...)
	cmd := exec.Command("git", fullArgs...)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
