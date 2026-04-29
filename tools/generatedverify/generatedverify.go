// Package generatedverify verifies checked-in generated artifacts from
// metadata-derived expectations.
package generatedverify

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

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

// Passed reports whether all expected generated artifacts are present and
// byte-for-byte current, every expected file is committed in HEAD, and no
// unexpected committed file lives under a directory exclusively owned by the
// generator. Tracking and reverse-enumeration checks are skipped when the
// project is not a git working tree (test fixtures), in which case only the
// content check runs.
func (r Result) Passed() bool {
	return len(r.Drifts) == 0
}

// driftKindUnexpected labels reverse-enumeration drift entries (committed
// files inside a generator-owned directory that are not in the expected
// manifest).
const driftKindUnexpected = "unexpected"

// Verify derives every generated artifact path and content from project
// metadata, then compares the result with the checked-in repository state.
//
// Two checks run in parallel against different data sources, deliberately:
//
//   - Content byte-equality is compared against the working tree so
//     developers can iterate locally without committing every step.
//   - Tracking and reverse-enumeration are compared against HEAD so a file
//     that was only `git add`-ed during CI cannot satisfy the gate, and stale
//     committed artifacts that fell out of the expected set are detected.
//
// The verifier deliberately does not consume generator stdout; it owns the
// expected artifact set.
func Verify(root, module string, project *metadata.ProjectMeta) (*Result, error) {
	artifacts, err := ExpectedArtifacts(root, module, project)
	if err != nil {
		return nil, err
	}

	expectedSet := make(map[string]Artifact, len(artifacts))
	for _, artifact := range artifacts {
		expectedSet[artifact.Path] = artifact
	}

	var committed map[string]bool
	managedDirs := managedDirRoots(artifacts)
	if hasGitMetadata(root) {
		committed, err = collectCommittedPaths(root, managedDirs, artifacts)
		if err != nil {
			return nil, fmt.Errorf("list committed generated artifacts: %w", err)
		}
	}

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
		if committed != nil && !committed[artifact.Path] {
			result.Drifts = append(result.Drifts, drift(artifact, "file is not committed in HEAD"))
		}
	}

	if committed != nil {
		for committedPath := range committed {
			if _, expected := expectedSet[committedPath]; expected {
				continue
			}
			if !underManagedDir(committedPath, managedDirs) {
				continue
			}
			result.Drifts = append(result.Drifts, Drift{
				AssemblyID: assemblyForManagedPath(committedPath, artifacts),
				Kind:       driftKindUnexpected,
				Path:       committedPath,
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

// managedDirRoots returns the directory paths that are owned exclusively by
// generators. Every committed file under one of these directories must be in
// the expected manifest; otherwise it is reverse-enumeration drift.
//
// Entrypoint files (e.g. cmd/<id>/main.go) share their parent directory with
// hand-written helpers (cmd/<id>/run.go), so they are managed file-by-file
// rather than by directory and do not contribute here.
func managedDirRoots(artifacts []Artifact) []string {
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		switch artifact.Kind {
		case "boundary", "metrics-schema":
			dir := path.Dir(artifact.Path)
			if dir == "." || dir == "" {
				continue
			}
			seen[dir] = true
		}
	}
	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

func underManagedDir(p string, managedDirs []string) bool {
	for _, dir := range managedDirs {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

func assemblyForManagedPath(p string, artifacts []Artifact) string {
	for _, artifact := range artifacts {
		switch artifact.Kind {
		case "boundary", "metrics-schema":
			dir := path.Dir(artifact.Path)
			if dir != "" && strings.HasPrefix(p, dir+"/") {
				return artifact.AssemblyID
			}
		}
	}
	return ""
}

// collectCommittedPaths returns the set of repo-relative paths that are
// committed in HEAD, scoped to (a) every path under a managed directory and
// (b) every expected entrypoint file. The two scopes are sufficient to power
// both the forward "is this expected artifact committed?" check and the
// reverse "is this committed file unexpected?" check.
func collectCommittedPaths(root string, managedDirs []string, artifacts []Artifact) (map[string]bool, error) {
	committed := map[string]bool{}

	for _, dir := range managedDirs {
		paths, err := gitListTreeHEAD(root, dir)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			committed[p] = true
		}
	}

	for _, artifact := range artifacts {
		if underManagedDir(artifact.Path, managedDirs) {
			continue
		}
		ok, err := gitHEADContains(root, artifact.Path)
		if err != nil {
			return nil, err
		}
		if ok {
			committed[artifact.Path] = true
		}
	}
	return committed, nil
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

// gitListTreeHEAD returns every path committed in HEAD under dir. An empty
// repo (no HEAD yet) returns an empty list rather than an error so the caller
// reports "file is not committed in HEAD" against each expected artifact
// instead of crashing the gate.
func gitListTreeHEAD(root, dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-tree", "-r", "--name-only", "HEAD", "--", dir)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("git ls-tree HEAD %s: %w", dir, err)
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, nil
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		paths = append(paths, string(line))
	}
	return paths, nil
}

func gitHEADContains(root, rel string) (bool, error) {
	cmd := exec.Command("git", "-C", root, "cat-file", "-e", "HEAD:"+rel)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("git cat-file HEAD:%s: %w", rel, err)
	}
	return true, nil
}
