// Package generatedverify verifies checked-in generated artifacts from
// metadata-derived expectations.
package generatedverify

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/metricschema"
)

// Artifact is one generated file that must be checked in exactly as derived
// from assembly metadata.
type Artifact struct {
	AssemblyID string
	Kind       string
	Path       string
	Content    []byte
}

// Drift describes one mismatch between metadata-derived expectations and the
// checked-in repository state.
type Drift struct {
	AssemblyID string
	Kind       string
	Path       string
	Message    string
}

// Result is the complete generated-artifact verification result.
type Result struct {
	Artifacts []Artifact
	Drifts    []Drift
}

// Passed reports whether all expected generated artifacts are present,
// byte-for-byte current, and tracked by git when git metadata is present.
func (r Result) Passed() bool {
	return len(r.Drifts) == 0
}

// Verify derives every generated artifact path and content from project
// metadata, then compares it with the checked-in filesystem. It deliberately
// does not consume generator stdout; the verifier owns the expected artifact
// set.
func Verify(root, module string, project *metadata.ProjectMeta) (*Result, error) {
	artifacts, err := ExpectedArtifacts(root, module, project)
	if err != nil {
		return nil, err
	}
	result := &Result{Artifacts: artifacts}
	checkGit := hasGitMetadata(root)
	for _, artifact := range artifacts {
		actual, err := os.ReadFile(absPath(root, artifact.Path))
		switch {
		case os.IsNotExist(err):
			result.Drifts = append(result.Drifts, drift(artifact, "file is missing"))
		case err != nil:
			return nil, fmt.Errorf("read generated artifact %s: %w", artifact.Path, err)
		case !bytes.Equal(actual, artifact.Content):
			result.Drifts = append(result.Drifts, drift(artifact, "content differs"))
		}
		if checkGit && !gitTracksFile(root, artifact.Path) {
			result.Drifts = append(result.Drifts, drift(artifact, "file is not tracked by git"))
		}
	}
	sort.Slice(result.Drifts, func(i, j int) bool {
		if result.Drifts[i].Path != result.Drifts[j].Path {
			return result.Drifts[i].Path < result.Drifts[j].Path
		}
		if result.Drifts[i].Kind != result.Drifts[j].Kind {
			return result.Drifts[i].Kind < result.Drifts[j].Kind
		}
		return result.Drifts[i].Message < result.Drifts[j].Message
	})
	return result, nil
}

// ExpectedArtifacts derives the complete generated-artifact manifest and
// in-memory content from assembly metadata.
func ExpectedArtifacts(root, module string, project *metadata.ProjectMeta) ([]Artifact, error) {
	if project == nil {
		return nil, fmt.Errorf("project metadata is nil")
	}
	if module == "" {
		return nil, fmt.Errorf("module path is required")
	}
	ids := make([]string, 0, len(project.Assemblies))
	for id := range project.Assemblies {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	gen := assembly.NewGenerator(project, module, root)
	artifacts := make([]Artifact, 0, len(ids)*3)
	for _, id := range ids {
		asm := project.Assemblies[id]
		if asm == nil {
			return nil, fmt.Errorf("assembly %q metadata is nil", id)
		}
		entrypointRel := AssemblyEntrypointPath(id, asm)
		entrypoint, err := gen.GenerateEntrypoint(id)
		if err != nil {
			return nil, fmt.Errorf("generate expected entrypoint for %q: %w", id, err)
		}
		artifacts = append(artifacts, Artifact{
			AssemblyID: id,
			Kind:       "assembly-entrypoint",
			Path:       entrypointRel,
			Content:    entrypoint,
		})

		boundary, err := gen.GenerateBoundary(id)
		if err != nil {
			return nil, fmt.Errorf("generate expected boundary for %q: %w", id, err)
		}
		artifacts = append(artifacts, Artifact{
			AssemblyID: id,
			Kind:       "boundary",
			Path:       filepath.ToSlash(filepath.Join("assemblies", id, "generated", "boundary.yaml")),
			Content:    boundary,
		})

		schema, err := metricschema.Build(root, project, id)
		if err != nil {
			return nil, fmt.Errorf("generate expected metrics schema for %q: %w", id, err)
		}
		metricsContent, err := metricschema.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("serialize expected metrics schema for %q: %w", id, err)
		}
		artifacts = append(artifacts, Artifact{
			AssemblyID: id,
			Kind:       "metrics-schema",
			Path:       filepath.ToSlash(filepath.Join("assemblies", id, "generated", "metrics-schema.yaml")),
			Content:    metricsContent,
		})
	}
	if err := validateArtifactPaths(root, artifacts); err != nil {
		return nil, err
	}
	return artifacts, nil
}

// AssemblyEntrypointPath returns the metadata-derived generated entrypoint path
// for an assembly.
func AssemblyEntrypointPath(assemblyID string, asm *metadata.AssemblyMeta) string {
	if asm != nil && asm.Build.Entrypoint != "" {
		return filepath.ToSlash(asm.Build.Entrypoint)
	}
	return filepath.ToSlash(filepath.Join("cmd", assemblyID, "main.go"))
}

func validateArtifactPaths(root string, artifacts []Artifact) error {
	for _, artifact := range artifacts {
		if filepath.IsAbs(artifact.Path) {
			return fmt.Errorf("%s for assembly %q must be repo-relative: %s",
				artifact.Kind, artifact.AssemblyID, artifact.Path)
		}
		if !governance.IsWithinRoot(root, absPath(root, artifact.Path)) {
			return fmt.Errorf("%s for assembly %q escapes project root: %s",
				artifact.Kind, artifact.AssemblyID, artifact.Path)
		}
	}
	return nil
}

func drift(artifact Artifact, message string) Drift {
	return Drift{
		AssemblyID: artifact.AssemblyID,
		Kind:       artifact.Kind,
		Path:       artifact.Path,
		Message:    message,
	}
}

func absPath(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}

func hasGitMetadata(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

func gitTracksFile(root, rel string) bool {
	cmd := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", rel)
	return cmd.Run() == nil
}
