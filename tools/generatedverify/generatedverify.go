// Package generatedverify verifies checked-in generated artifacts from
// project-derived expectations.
package generatedverify

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/depgraph"
	"github.com/ghbvf/gocell/tools/generatedcatalog"
	"github.com/ghbvf/gocell/tools/metricschema"
)

// Artifact is one generated file that must be checked in exactly as derived
// from project inputs.
type Artifact struct {
	AssemblyID string
	Kind       string
	Path       string
	Content    []byte
}

// Drift describes one mismatch between project-derived expectations and the
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

// Passed reports whether all expected generated artifacts are present and
// byte-for-byte current, every expected file is committed in HEAD, and no
// other committed file in the work tree carries a gocell generator header.
// Tracking and reverse-enumeration checks are skipped when the project is
// not a git working tree (test fixtures), in which case only the content
// check runs.
func (r Result) Passed() bool {
	return len(r.Drifts) == 0
}

// driftKindUnexpected labels reverse-enumeration drift entries (committed
// files that carry a gocell generator header but are not in the expected
// manifest — orphans from removed assemblies, renamed entrypoints, or
// generators that have been retired).
const driftKindUnexpected = "unexpected"

// Verify derives every generated artifact path and content from project inputs,
// then compares the result with the checked-in repository state.
//
// Two checks run against different data sources, deliberately:
//
//   - Content byte-equality is compared against the working tree so
//     developers can iterate locally without committing every step.
//   - Tracking and reverse-enumeration are compared against HEAD so a file
//     that was only `git add`-ed during CI cannot satisfy the gate, and
//     committed files that fell out of the expected set are detected
//     wherever they live in the tree.
//
// Reverse enumeration is header-driven: every committed file at HEAD whose
// first line is a gocell generator sentinel (governance.GoGeneratedPrefix
// or governance.YAMLGeneratedPrefix) is a candidate. Anything outside the
// expected manifest is drift, regardless of directory. This makes
// assembly.yaml the single source of truth for what may live under any
// generator-owned path.
func Verify(root, module string, project *metadata.ProjectMeta) (*Result, error) {
	artifacts, err := ExpectedArtifacts(root, module, project)
	if err != nil {
		return nil, err
	}

	expectedSet := make(map[string]Artifact, len(artifacts))
	for _, artifact := range artifacts {
		expectedSet[artifact.Path] = artifact
	}

	gitTracked := governance.HasGitMetadata(root)

	result := &Result{Artifacts: artifacts}

	for _, artifact := range artifacts {
		actual, readErr := os.ReadFile(absPath(root, artifact.Path))
		switch {
		case os.IsNotExist(readErr):
			result.Drifts = append(result.Drifts, drift(artifact, "file is missing"))
		case readErr != nil:
			return nil, fmt.Errorf("read generated artifact %s: %w", artifact.Path, readErr)
		case !bytes.Equal(actual, artifact.Content):
			result.Drifts = append(result.Drifts, drift(artifact, "content differs"))
		}
		if !gitTracked {
			continue
		}
		committed, err := governance.CommittedInHEAD(root, artifact.Path)
		if err != nil {
			return nil, fmt.Errorf("check committed artifact %s: %w", artifact.Path, err)
		}
		if !committed {
			result.Drifts = append(result.Drifts, drift(artifact, "file is not committed in HEAD"))
		}
	}

	if gitTracked {
		generatedPaths, err := governance.ListGeneratedInHEAD(root)
		if err != nil {
			return nil, fmt.Errorf("list generator-marked files in HEAD: %w", err)
		}
		for _, p := range generatedPaths {
			if _, expected := expectedSet[p]; expected {
				continue
			}
			result.Drifts = append(result.Drifts, Drift{
				AssemblyID: assemblyForOrphanPath(p),
				Kind:       driftKindUnexpected,
				Path:       p,
				Message:    "file is not in expected manifest",
			})
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
// in-memory content from project inputs.
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
	artifacts := make([]Artifact, 0, len(ids)*3+1)
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
	catalog, err := expectedCatalogGraphArtifact(root, module)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, catalog)
	if err := validateArtifactPaths(root, artifacts); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func expectedCatalogGraphArtifact(root, module string) (Artifact, error) {
	graph, err := depgraph.Load(depgraph.LoadOptions{Dir: root}, "./...")
	if err != nil {
		return Artifact{}, fmt.Errorf("generate expected catalog graph: %w", err)
	}
	content, err := generatedcatalog.EmitFile(generatedcatalog.CorebundlePackage, module, graph)
	if err != nil {
		return Artifact{}, fmt.Errorf("serialize expected catalog graph: %w", err)
	}
	return Artifact{
		Kind:    "catalog-graph",
		Path:    generatedcatalog.CorebundlePath,
		Content: content,
	}, nil
}

// AssemblyEntrypointPath returns the metadata-derived generated entrypoint path
// for an assembly.
func AssemblyEntrypointPath(assemblyID string, asm *metadata.AssemblyMeta) string {
	if asm != nil && asm.Build.Entrypoint != "" {
		return filepath.ToSlash(asm.Build.Entrypoint)
	}
	return filepath.ToSlash(filepath.Join("cmd", assemblyID, "main.go"))
}

// assemblyForOrphanPath best-effort-derives the AssemblyID for an orphan
// reverse-enumeration drift entry. assemblies/<id>/generated/... paths
// always belong to assembly <id>; entrypoint orphans (e.g. cmd/<id>/main.go
// left behind after an entrypoint rename) cannot be tied to a current
// manifest assembly and surface with an empty AssemblyID — the drift
// message itself is enough to point operators at the path.
func assemblyForOrphanPath(p string) string {
	const prefix = "assemblies/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	rest := p[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return ""
	}
	return rest[:slash]
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
