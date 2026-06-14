package generatedverify

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/tools/codegen/sagacoveragegen"
)

const fixtureModule = "example.com/generatedfixture"

func TestExpectedArtifactsDerivesManifestFromMetadata(t *testing.T) {
	root, project := newGeneratedFixture(t)

	artifacts, err := ExpectedArtifacts(t.Context(), root, fixtureModule, project)
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

// TestExpectedArtifactsGatesSagaCoverageOnPackagePresence proves the
// non-metadata-derived SAGA-STATUS-FANOUT-COVERAGE-01 entry
// (terminal_coverage_gen.go) is emitted only when its target package directory
// is present in the project tree: absent for the bare fixture (so the synthetic
// fixtures and downstream consumer modules see no phantom artifact), present
// once the directory exists — with content byte-identical to
// sagacoveragegen.Render().
func TestExpectedArtifactsGatesSagaCoverageOnPackagePresence(t *testing.T) {
	root, project := newGeneratedFixture(t)

	// Absent: the bare fixture has no kernel/saga/sagajournaltest package.
	artifacts, err := ExpectedArtifacts(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)
	assert.NotContains(t, artifactKinds(artifacts), "saga-coverage-gen",
		"saga-coverage artifact must be omitted when the target package is absent")

	// Present: materialize the target file (and thus its package directory); the
	// gate now opens and the entry appears with rendered content.
	writeFile(t, root, sagaCoverageGenRel, []byte("// placeholder\n"))
	artifacts, err = ExpectedArtifacts(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	var got *Artifact
	for i := range artifacts {
		if artifacts[i].Kind == "saga-coverage-gen" {
			got = &artifacts[i]
			break
		}
	}
	require.NotNil(t, got, "saga-coverage artifact must be emitted once the package directory exists")
	assert.Equal(t, sagaCoverageGenRel, got.Path)

	want, err := sagacoveragegen.Render()
	require.NoError(t, err)
	assert.Equal(t, want.TerminalCoverageGo, got.Content,
		"saga-coverage artifact content must be byte-identical to sagacoveragegen.Render()")
}

func TestVerifyPassesWhenExpectedFilesAreCommitted(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts := writeExpectedArtifacts(t, root, project)
	gitInitAndCommit(t, root, artifactPaths(artifacts))

	result, err := Verify(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	assert.True(t, result.Passed())
	assert.Empty(t, result.Drifts)
	assert.Len(t, result.Artifacts, 3)
}

func TestVerifyReportsMissingAndChangedArtifacts(t *testing.T) {
	root, project := newGeneratedFixture(t)
	artifacts, err := ExpectedArtifacts(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	staleEntrypoint := append([]byte(nil), artifacts[0].Content...)
	staleEntrypoint = append(staleEntrypoint, []byte("\n// stale generated main\n")...)
	writeFile(t, root, artifacts[0].Path, staleEntrypoint)

	result, err := Verify(t.Context(), root, fixtureModule, project)
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

	result, err := Verify(t.Context(), root, fixtureModule, project)
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

	result, err := Verify(t.Context(), root, fixtureModule, project)
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

	result, err := Verify(t.Context(), root, fixtureModule, project)
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

	result, err := Verify(t.Context(), root, fixtureModule, project)
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
		[]byte("// Code generated by gocell generate assembly. DO NOT EDIT.\n//go:build ignore\n\npackage main\n"))

	allCommitted := append(artifactPaths(artifacts), orphanEntrypoint)
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(t.Context(), root, fixtureModule, project)
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

	result, err := Verify(t.Context(), root, fixtureModule, project)
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
		[]byte("// Code generated by gocell generate experimental. DO NOT EDIT.\n//go:build ignore\n\npackage generated\n"))

	allCommitted := append(artifactPaths(artifacts), rogue)
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(t.Context(), root, fixtureModule, project)
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
	allCommitted := append(artifactPaths(artifacts), "cmd/fixture/run.go", "go.mod", "framework/runtime/shutdown/shutdown.go")
	gitInitAndCommit(t, root, allCommitted)

	result, err := Verify(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	assert.True(t, result.Passed(), "hand-written sibling under cmd/<id>/ must not be flagged: %+v", result.Drifts)
}

func TestExpectedArtifactsRejectsInvalidInputs(t *testing.T) {
	root := t.TempDir()

	_, err := ExpectedArtifacts(t.Context(), root, fixtureModule, nil)
	require.ErrorContains(t, err, "project metadata is nil")

	_, err = ExpectedArtifacts(t.Context(), root, "", &metadata.ProjectMeta{})
	require.ErrorContains(t, err, "module path is required")

	_, err = ExpectedArtifacts(t.Context(), root, fixtureModule, &metadata.ProjectMeta{
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
		"assemblies/fixture/generated/boundary.yaml":      "fixture",
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

// newGeneratedFixtureWithCells creates a fixture with an assembly that has
// cells>0, triggering the assembly-modules-gen Kind in ExpectedArtifacts.
//
// The placeholder cell carries GoStructName so modules_gen.go is emitted.
// Dir and File are set so expectedCellgenArtifacts can render cell_gen.go
// without hitting a missing-package-name error. The underlying cell.yaml
// file is also written so cellgen's BuildCellSpec can read it.
func newGeneratedFixtureWithCells(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()

	root, project := newGeneratedFixture(t)

	// Write minimal cell.yaml on disk so cellgen template sources work.
	writeFile(t, root, "cells/placeholder/cell.yaml", []byte(
		"id: placeholder\ntype: core\nconsistencyLevel: L0\n"+
			"owner:\n  team: fixture\n  role: test\n"+
			"schema:\n  primary: placeholder_table\n"+
			"verify:\n  smoke: []\ngoStructName: Placeholder\n",
	))

	// cmd/fixture/cell_module.go: CellModule interface + PlaceholderModule stub so
	// modules_gen.go (which references both) compiles when metricschema.Build
	// calls go/packages.Load against the entrypoint package.
	writeFile(t, root, "cmd/fixture/cell_module.go", []byte(`package main

type CellModule interface {
	ID() string
}

// PlaceholderModule satisfies the generated CellModule factory list
// (cells: [placeholder], goStructName: Placeholder).
type PlaceholderModule struct{}

func (PlaceholderModule) ID() string { return "placeholder" }
`))

	// Add a placeholder cell with GoStructName and Dir/File set so cellgen
	// can derive Package and SourceFile for the template.
	project.Cells[metadatatest.NewCellID("placeholder")] = &metadata.CellMeta{
		ID:           metadatatest.NewCellID("placeholder"),
		Type:         "core",
		GoStructName: metadata.MustNewGoIdentifier("Placeholder"),
		Dir:          "placeholder",                 // used as Go package name
		File:         "cells/placeholder/cell.yaml", // used as SourceFile in header
	}
	// Update the fixture assembly to include the placeholder cell.
	project.Assemblies["fixture"] = &metadata.AssemblyMeta{
		ID:    "fixture",
		Cells: metadata.CellRefs(metadatatest.NewCellID("placeholder")),
		Build: metadata.BuildMeta{
			Entrypoint: "cmd/fixture/main.go",
			Binary:     "bin/fixture",
		},
		// File mirrors the post-M1 (#1082) Locator-emitted path so
		// AssemblyGeneratedDir resolves "assemblies/fixture/generated".
		File: "assemblies/fixture/assembly.yaml",
	}
	return root, project
}

// TestExpectedArtifactsWithCellsIncludesModulesGen verifies that an assembly
// with cells>0 emits the assembly-modules-gen Kind. With the placeholder cell
// also having GoStructName set, the total is 5 artifacts:
// assembly-entrypoint, boundary, assembly-modules-gen, metrics-schema, cell-gen.
func TestExpectedArtifactsWithCellsIncludesModulesGen(t *testing.T) {
	root, project := newGeneratedFixtureWithCells(t)

	artifacts, err := ExpectedArtifacts(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	// Collect kinds to verify assembly-modules-gen is present.
	kinds := artifactKinds(artifacts)
	assert.Contains(t, kinds, "assembly-modules-gen",
		"expected assembly-modules-gen kind in manifest; got %v", kinds)

	// Locate the assembly-modules-gen artifact.
	var modulesArtifact *Artifact
	for i := range artifacts {
		if artifacts[i].Kind == "assembly-modules-gen" {
			modulesArtifact = &artifacts[i]
			break
		}
	}
	require.NotNil(t, modulesArtifact, "assembly-modules-gen artifact not found in manifest")
	assert.Equal(t, "cmd/fixture/modules_gen.go", modulesArtifact.Path)
	assert.Contains(t, string(modulesArtifact.Content), "generatedCellModules")
	assert.Contains(t, string(modulesArtifact.Content), "PlaceholderModule")
}

// TestVerifyDetectsTamperedModulesGen verifies that altering modules_gen.go
// after it was correctly generated is reported as content-differs drift.
func TestVerifyDetectsTamperedModulesGen(t *testing.T) {
	root, project := newGeneratedFixtureWithCells(t)
	artifacts := writeExpectedArtifacts(t, root, project)

	// Tamper with modules_gen.go.
	for _, a := range artifacts {
		if a.Kind == "assembly-modules-gen" {
			tampered := append([]byte(nil), a.Content...)
			tampered = append(tampered, []byte("\n// tampered\n")...)
			writeFile(t, root, a.Path, tampered)
		}
	}

	result, err := Verify(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	var found bool
	for _, d := range result.Drifts {
		if d.Kind == "assembly-modules-gen" && d.Message == "content differs" {
			found = true
		}
	}
	assert.True(t, found, "expected assembly-modules-gen content-differs drift; got %+v", result.Drifts)
}

// TestVerifyDetectsMissingModulesGen verifies that a missing modules_gen.go
// is reported as file-is-missing drift.
func TestVerifyDetectsMissingModulesGen(t *testing.T) {
	root, project := newGeneratedFixtureWithCells(t)
	writeExpectedArtifacts(t, root, project)

	// Remove modules_gen.go.
	require.NoError(t, os.Remove(filepath.Join(root, "cmd", "fixture", "modules_gen.go")))

	result, err := Verify(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	var found bool
	for _, d := range result.Drifts {
		if d.Kind == "assembly-modules-gen" && d.Message == "file is missing" {
			found = true
		}
	}
	assert.True(t, found, "expected assembly-modules-gen file-is-missing drift; got %+v", result.Drifts)
}

// TestExpectedArtifactsIncludesTSContractArtifacts proves ExpectedArtifacts
// derives the TS manifest entries (CONTRACTGEN-TS-EMIT-FUNNEL-01): every codegen
// contract that emits TS gets a per-contract generated-ts/.../types.ts (kind
// contract-gen) and the cross-contract generated-ts/index.ts barrel (kind
// contract-gen-ts-barrel). Without a contract in the fixture (the base
// TestExpectedArtifactsDerivesManifestFromMetadata) the TS branch is never
// exercised — this is the gap F7 closes.
func TestExpectedArtifactsIncludesTSContractArtifacts(t *testing.T) {
	root, project := newGeneratedFixtureWithContract(t)

	artifacts, err := ExpectedArtifacts(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	const (
		typesPath  = "generated-ts/contracts/http/fixture/ping/v1/types.ts"
		barrelPath = "generated-ts/index.ts"
	)
	typesKind, hasTypes := artifactKindForPath(artifacts, typesPath)
	require.True(t, hasTypes, "expected per-contract TS artifact %q in manifest; got %v", typesPath, artifactPaths(artifacts))
	assert.Equal(t, "contract-gen", typesKind)

	barrelKind, hasBarrel := artifactKindForPath(artifacts, barrelPath)
	require.True(t, hasBarrel, "expected TS barrel artifact %q in manifest; got %v", barrelPath, artifactPaths(artifacts))
	assert.Equal(t, "contract-gen-ts-barrel", barrelKind)

	// The barrel must re-export the contract's namespace alias.
	for _, a := range artifacts {
		if a.Path == barrelPath {
			assert.Contains(t, string(a.Content), "export * as httpFixturePingV1 from")
		}
	}
}

// TestVerifyDetectsMissingTSBarrel proves a missing committed generated-ts/index.ts
// is reported as file-is-missing drift.
func TestVerifyDetectsMissingTSBarrel(t *testing.T) {
	root, project := newGeneratedFixtureWithContract(t)
	writeExpectedArtifacts(t, root, project)

	require.NoError(t, os.Remove(filepath.Join(root, "generated-ts", "index.ts")))

	result, err := Verify(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	var found bool
	for _, d := range result.Drifts {
		if d.Kind == "contract-gen-ts-barrel" && d.Message == "file is missing" {
			found = true
		}
	}
	assert.True(t, found, "expected contract-gen-ts-barrel file-is-missing drift; got %+v", result.Drifts)
}

// TestVerifyDetectsTamperedTSBarrel proves a hand-edited generated-ts/index.ts is
// reported as content-differs drift.
func TestVerifyDetectsTamperedTSBarrel(t *testing.T) {
	root, project := newGeneratedFixtureWithContract(t)
	artifacts := writeExpectedArtifacts(t, root, project)

	for _, a := range artifacts {
		if a.Kind == "contract-gen-ts-barrel" {
			tampered := append([]byte(nil), a.Content...)
			tampered = append(tampered, []byte("\nexport * as injected from './evil';\n")...)
			writeFile(t, root, a.Path, tampered)
		}
	}

	result, err := Verify(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)

	assert.False(t, result.Passed())
	var found bool
	for _, d := range result.Drifts {
		if d.Kind == "contract-gen-ts-barrel" && d.Message == "content differs" {
			found = true
		}
	}
	assert.True(t, found, "expected contract-gen-ts-barrel content-differs drift; got %+v", result.Drifts)
}

// artifactKindForPath returns the Kind of the artifact at the given slash path.
func artifactKindForPath(artifacts []Artifact, path string) (string, bool) {
	for _, a := range artifacts {
		if a.Path == path {
			return a.Kind, true
		}
	}
	return "", false
}

// newGeneratedFixtureWithContract extends newGeneratedFixture with a minimal
// codegen HTTP contract (contract.yaml + request/response/error schemas) on disk
// and merges its parsed ContractMeta into the project, so ExpectedArtifacts
// exercises the contractgen + TS-emit branches. The contract files are parsed
// (not hand-built) so the ContractMeta stays faithful to what `gocell` produces.
func newGeneratedFixtureWithContract(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()

	root, project := newGeneratedFixture(t)

	writeFile(t, root, "contracts/http/fixture/ping/v1/contract.yaml", []byte(`id: http.fixture.ping.v1
kind: http
ownerCell: fixturecell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: fixturecell
  http:
    method: POST
    path: /api/v1/fixture/ping/
    successStatus: 201
    noContent: false
    responses:
      400:
        description: Bad Request
        schemaRef: "../../../../shared/errors/error-response-v1.schema.json"
      500:
        description: Internal Server Error
        schemaRef: "../../../../shared/errors/error-response-v1.schema.json"
schemaRefs:
  request: request.schema.json
  response: response.schema.json
codegen: true
`))
	writeFile(t, root, "contracts/http/fixture/ping/v1/request.schema.json", []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "http.fixture.ping.v1.request",
  "type": "object",
  "properties": {
    "message": { "type": "string" }
  },
  "required": ["message"],
  "additionalProperties": false
}
`))
	writeFile(t, root, "contracts/http/fixture/ping/v1/response.schema.json", []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "http.fixture.ping.v1.response",
  "type": "object",
  "properties": {
    "ok": { "type": "boolean" }
  },
  "required": ["ok"]
}
`))
	writeFile(t, root, "contracts/shared/errors/error-response-v1.schema.json", []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://gocell.dev/schemas/errors/error-response-v1.schema.json",
  "title": "GoCell HTTP error response",
  "type": "object",
  "required": ["error"],
  "additionalProperties": false,
  "properties": {
    "error": {
      "type": "object",
      "required": ["code", "message", "details"],
      "additionalProperties": false,
      "properties": {
        "code": {"type": "string", "pattern": "^ERR_[A-Z0-9_]+$"},
        "message": {"type": "string"},
        "details": {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["key", "value"],
            "additionalProperties": false,
            "properties": {
              "key": {"type": "string"},
              "value": {"type": ["string", "number", "boolean"]}
            }
          }
        },
        "request_id": {"type": "string"}
      }
    }
  }
}
`))

	parsed, err := metadata.NewParser(root).Parse()
	require.NoError(t, err)
	for id, c := range parsed.Contracts {
		project.Contracts[id] = c
	}
	require.Contains(t, project.Contracts, "http.fixture.ping.v1", "parser must discover the fixture contract")

	return root, project
}

func newGeneratedFixture(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()

	root := t.TempDir()
	writeFile(t, root, "go.mod", []byte("module "+fixtureModule+"\n\ngo 1.25.0\n"))
	writeFile(t, root, "framework/kernel/depgraph/depgraph.go", []byte(`package depgraph

type Graph struct {
	Module   string
	Packages []*Node
}

type Node struct {
	ID       string
	Layer    string
	CellID   string
	SliceID  string
	TestOnly bool
	Imports  []string
}

func FromNodes(module string, nodes []*Node) *Graph {
	return &Graph{Module: module, Packages: nodes}
}
`))
	writeFile(t, root, "framework/runtime/shutdown/shutdown.go", []byte(`package shutdown

import "context"

func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}
`))
	// The assembly main.go template imports {{.Module}}/framework/runtime/observability/logging
	// for the sink-side redaction seal (SLOG-HANDLER-SEALED-FUNNEL-01 A3 generated
	// segment). Provide a minimal stub so the generated main compiles.
	writeFile(t, root, "framework/runtime/observability/logging/logging.go", []byte(`package logging

import (
	"log/slog"
	"os"
)

type Format string

const FormatJSON Format = "json"

type Options struct {
	Format Format
}

func NewHandler(opts Options) slog.Handler {
	return slog.NewJSONHandler(os.Stdout, nil)
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
		Cells: metadata.CellRefs(),
		Build: metadata.BuildMeta{
			Entrypoint: "cmd/fixture/main.go",
			Binary:     "bin/fixture",
		},
		// File is set so metadata.AssemblyGeneratedDir derives
		// "assemblies/fixture/generated" (path.Dir(File)+"/generated").
		// Before M1 (#1082), the helper had an internal fallback that
		// defaulted to this path when File was empty; the fallback was
		// removed when the Locator funnel made File authoritative.
		File: "assemblies/fixture/assembly.yaml",
	}
	return root, project
}

func writeExpectedArtifacts(t *testing.T, root string, project *metadata.ProjectMeta) []Artifact {
	t.Helper()

	artifacts, err := ExpectedArtifacts(t.Context(), root, fixtureModule, project)
	require.NoError(t, err)
	for _, artifact := range artifacts {
		writeFile(t, root, artifact.Path, artifact.Content)
	}

	// The catalog artifact describes the package graph, and generated entrypoints
	// are themselves Go packages. Recompute after the first write so fixtures
	// mirror a checked-in tree where generated files already exist.
	artifacts, err = ExpectedArtifacts(t.Context(), root, fixtureModule, project)
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
