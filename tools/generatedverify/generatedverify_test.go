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

func TestVerifyPassesWhenExpectedFilesAreCommitted(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)
	gitInitAndCommit(t, root, artifactPaths(artifacts))

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

func TestVerifyReportsUncommittedArtifactsInsideGitRepo(t *testing.T) {
	root, project := newGeneratedFixture(t)
	writeExpectedArtifacts(t, root, project)
	gitRun(t, root, "init", "-q")
	gitConfigUser(t, root)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	assert.Equal(t, []Drift{
		{
			AssemblyID: "fixture",
			Kind:       "boundary",
			Path:       "assemblies/fixture/generated/boundary.yaml",
			Message:    "file is not committed in HEAD",
		},
		{
			AssemblyID: "fixture",
			Kind:       "metrics-schema",
			Path:       "assemblies/fixture/generated/metrics-schema.yaml",
			Message:    "file is not committed in HEAD",
		},
		{
			AssemblyID: "fixture",
			Kind:       "assembly-entrypoint",
			Path:       "cmd/fixture/main.go",
			Message:    "file is not committed in HEAD",
		},
	}, result.Drifts)
}

// TestVerifyRejectsStagedButUncommittedArtifact covers the CI-during-staging
// attack from PR #332 review report 1: a malicious or buggy CI step could
// `git add` regenerated content without committing, and the previous gate
// (which probed `git ls-files`) would treat the staged file as tracked. The
// fail-closed gate must require the file to exist in HEAD.
func TestVerifyRejectsStagedButUncommittedArtifact(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)
	gitRun(t, root, "init", "-q")
	gitConfigUser(t, root)
	gitAdd(t, root, artifactPaths(artifacts))

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	for _, d := range result.Drifts {
		assert.Equal(t, "file is not committed in HEAD", d.Message,
			"every drift must be uncommitted-in-HEAD; got %+v", d)
	}
	assert.Len(t, result.Drifts, 3)
}

// TestVerifyDetectsOrphanedAssemblyGeneratedArtifact covers the
// reverse-enumeration gap from the PR #332 round-2 review: an old generated
// file (boundary.yaml / metrics-schema.yaml) committed under
// assemblies/<id>/generated/ must be flagged when it is no longer in the
// metadata-derived expected set, even when assembly <id> still exists.
// Header-driven enumeration also catches the harder case of a deleted
// assembly, which TestVerifyDetectsOrphanedFileFromRemovedAssembly covers
// directly.
func TestVerifyDetectsOrphanedAssemblyGeneratedArtifact(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)

	stalePath := "assemblies/fixture/generated/legacy-boundary.yaml"
	writeFile(t, root, stalePath,
		[]byte("# Generated by gocell generate legacy. DO NOT EDIT.\n# stale generated artifact, no longer in manifest\n"))

	allCommitted := append(artifactPaths(artifacts), stalePath)
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	require.Len(t, result.Drifts, 1)
	assert.Equal(t, Drift{
		AssemblyID: "fixture",
		Kind:       driftKindUnexpected,
		Path:       stalePath,
		Message:    "file is not in expected manifest",
	}, result.Drifts[0])
}

// TestVerifyDetectsOrphanedFileFromRemovedAssembly covers the case where an
// assembly is removed from the manifest entirely but its generated
// directory is still committed. assemblies/<old>/generated/* falls outside
// the current expected set, but header-driven reverse enumeration still
// catches every committed file with a gocell sentinel.
func TestVerifyDetectsOrphanedFileFromRemovedAssembly(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)

	orphanPath := "assemblies/legacy/generated/boundary.yaml"
	writeFile(t, root, orphanPath,
		[]byte("# Generated by gocell generate assembly. DO NOT EDIT.\nassemblyId: legacy\n"))

	allCommitted := append(artifactPaths(artifacts), orphanPath)
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	require.Len(t, result.Drifts, 1)
	assert.Equal(t, Drift{
		AssemblyID: "legacy",
		Kind:       driftKindUnexpected,
		Path:       orphanPath,
		Message:    "file is not in expected manifest",
	}, result.Drifts[0])
}

// TestVerifyDetectsRenamedEntrypointLeftBehind covers the case where
// build.entrypoint moves to a new path but the old cmd/<id>/main.go is
// still committed with the gocell generator header. Without
// header-driven reverse enumeration the gate would only check the new
// path and ignore the old entrypoint forever.
func TestVerifyDetectsRenamedEntrypointLeftBehind(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)

	orphanEntrypoint := "cmd/legacyfixture/main.go"
	writeFile(t, root, orphanEntrypoint,
		[]byte("// Code generated by gocell generate assembly. DO NOT EDIT.\npackage main\n"))

	allCommitted := append(artifactPaths(artifacts), orphanEntrypoint)
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	require.Len(t, result.Drifts, 1)
	assert.Equal(t, Drift{
		AssemblyID: "",
		Kind:       driftKindUnexpected,
		Path:       orphanEntrypoint,
		Message:    "file is not in expected manifest",
	}, result.Drifts[0])
}

// TestVerifyIgnoresHandwrittenFileInGeneratedDir confirms reverse
// enumeration is header-driven, not directory-driven. A hand-written file
// committed under assemblies/<id>/generated/ without the gocell sentinel
// is operator territory; the gate must not treat it as drift.
func TestVerifyIgnoresHandwrittenFileInGeneratedDir(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)

	notes := "assemblies/fixture/generated/NOTES.md"
	writeFile(t, root, notes, []byte("hand-written notes; not generated\n"))

	allCommitted := append(artifactPaths(artifacts), notes)
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)
	assert.True(t, result.Passed(),
		"hand-written file without generator header must not surface as drift: %+v",
		result.Drifts)
}

// TestVerifyDetectsGeneratorHeaderOutsideKnownDirs ensures the policy is
// "any committed file with a gocell header is governed", not "files under
// a few hard-coded directories are governed". A future generator that
// writes to a new location must still be caught by the manifest-or-orphan
// check.
func TestVerifyDetectsGeneratorHeaderOutsideKnownDirs(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)

	rogue := "internal/generated/rogue.go"
	writeFile(t, root, rogue,
		[]byte("// Code generated by gocell generate experimental. DO NOT EDIT.\npackage generated\n"))

	allCommitted := append(artifactPaths(artifacts), rogue)
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	require.Len(t, result.Drifts, 1)
	assert.Equal(t, Drift{
		AssemblyID: "",
		Kind:       driftKindUnexpected,
		Path:       rogue,
		Message:    "file is not in expected manifest",
	}, result.Drifts[0])
}

// TestVerifyAllowsHandwrittenSiblingOfEntrypoint guards the rule that an
// entrypoint is managed file-by-file rather than directory-wide: cmd/<id>/
// can host hand-written helpers (e.g. cmd/corebundle/run.go) without
// triggering reverse-enumeration drift.
func TestVerifyAllowsHandwrittenSiblingOfEntrypoint(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)

	// cmd/fixture/run.go is created by newGeneratedFixture and is hand-written.
	allCommitted := append(artifactPaths(artifacts), "cmd/fixture/run.go", "go.mod", "runtime/shutdown/shutdown.go")
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(root, fixtureModule, project)
	require.NoError(t, err)

	assert.True(t, result.Passed(), "hand-written sibling under cmd/<id>/ must not be flagged: %+v", result.Drifts)
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

func TestAssemblyForOrphanPath(t *testing.T) {
	cases := map[string]string{
		"assemblies/fixture/generated/boundary.yaml":     "fixture",
		"assemblies/legacy/generated/metrics-schema.yaml": "legacy",
		"cmd/orphan/main.go":                              "",
		"docs/notes.md":                                   "",
		"assemblies/":                                     "",
		"assemblies":                                      "",
	}
	for path, want := range cases {
		assert.Equal(t, want, assemblyForOrphanPath(path), "path=%s", path)
	}
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

func gitConfigUser(t *testing.T, root string) {
	t.Helper()

	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	// commit.gpgsign defaults to false in tests, but be explicit so
	// host-level signing config doesn't make `git commit` block.
	gitRun(t, root, "config", "commit.gpgsign", "false")
}

func gitAdd(t *testing.T, root string, paths []string) {
	t.Helper()

	args := append([]string{"--"}, paths...)
	gitRun(t, root, "add", args...)
}

func gitInitAndCommit(t *testing.T, root string, paths []string) {
	t.Helper()

	gitRun(t, root, "init", "-q")
	gitConfigUser(t, root)
	gitAdd(t, root, paths)
	gitRun(t, root, "commit", "-q", "-m", "fixture", "--no-gpg-sign")
}
