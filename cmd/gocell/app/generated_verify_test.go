package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunVerifyGenerated_SyntheticProjectPasses(t *testing.T) {
	root := newGeneratedVerifyAppFixture(t)
	withWorkingDir(t, root)

	generateAllAppFixtureArtifacts(t)

	var err error
	out := captureStdout(t, func() {
		err = runVerify([]string{"generated"})
	})

	require.NoError(t, err)
	// fixture has cells: [placeholder] (goStructName set) so modules_gen.go +
	// cell_gen.go are also emitted → 5 artifacts total
	assert.Contains(t, out, "Generated artifacts verified: 5 files")
}

func TestRunVerifyGenerated_ReportsDrift(t *testing.T) {
	root := newGeneratedVerifyAppFixture(t)
	withWorkingDir(t, root)

	generateAllAppFixtureArtifacts(t)
	writeAppFixtureFile(t, root, "assemblies/fixture/generated/boundary.yaml", []byte("stale\n"))

	err := runVerify([]string{"generated"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "verify generated: 1 drift(s)")
	assert.Contains(t, err.Error(), "make generate")
}

func newGeneratedVerifyAppFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeAppFixtureFile(t, root, "go.mod", []byte("module example.com/generatedfixture\n\ngo 1.25.0\n"))
	writeAppFixtureFile(t, root, "kernel/depgraph/depgraph.go", []byte(`package depgraph

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
	// B2.1: assembly.yaml with cells: [placeholder] + owner + deployTemplate: k8s
	// to satisfy schema (minItems:1, owner required, enum deployTemplate).
	writeAppFixtureFile(t, root, "assemblies/fixture/assembly.yaml", []byte(`id: fixture
cells:
  - placeholder
owner:
  team: fixture
  role: test
build:
  entrypoint: cmd/fixture/main.go
  binary: fixture
  deployTemplate: k8s
`))
	// B2.1: minimal cell.yaml for the placeholder cell (goStructName required for
	// modules_gen.go factory derivation).
	writeAppFixtureFile(t, root, "cells/placeholder/cell.yaml", []byte(`id: placeholder
type: core
consistencyLevel: L0
owner:
  team: fixture
  role: test
schema:
  primary: placeholder_table
verify:
  smoke: []
goStructName: Placeholder
`))
	writeAppFixtureFile(t, root, "runtime/shutdown/shutdown.go", []byte(`package shutdown

import "context"

func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}
`))
	writeAppFixtureFile(t, root, "cmd/fixture/run.go", []byte(`package main

import "context"

func runFixture(context.Context, string, []string) error {
	return nil
}
`))
	// K#10: gocell generate assembly now also emits cmd/{id}/modules_gen.go
	// which references a CellModule type and {GoStructName}Module struct names.
	// Provide a minimal type definition and a PlaceholderModule stub so the
	// generated file compiles. Real assemblies declare CellModule with the full
	// Provide signature in cmd/{id}/cell_module.go; the fixture only needs the
	// types to satisfy modules_gen.go's reference.
	writeAppFixtureFile(t, root, "cmd/fixture/cell_module.go", []byte(`package main

type CellModule interface {
	ID() string
}

// PlaceholderModule is a minimal stub satisfying the generated CellModule list
// for the fixture assembly (cells: [placeholder], goStructName: Placeholder).
type PlaceholderModule struct{}

func (PlaceholderModule) ID() string { return "placeholder" }
`))
	return root
}

func generateAllAppFixtureArtifacts(t *testing.T) {
	t.Helper()

	require.NoError(t, runGenerate([]string{"assembly", "--id=fixture"}))
	require.NoError(t, runGenerate([]string{"metrics-schema", "--id=fixture"}))
	// The placeholder cell has goStructName set so it also needs cell_gen.go
	// in the expected manifest; generate it so verify passes.
	require.NoError(t, runGenerate([]string{"cell", "placeholder"}))
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(orig))
	})
}

func writeAppFixtureFile(t *testing.T, root, rel string, content []byte) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, content, 0o644))
}
