// run.go is the handwritten runtime half behind the generated assembly
// entrypoint for {{.ID}}. The generated main.go owns the assembly ID and
// cell order; this file owns environment loading and runtime option wiring.
//
// COMPOSITION-API scaffold (build.compositionAPI: true): this assembly composes
// cells from other Go modules, so `gocell generate assembly --id={{.ID}}` emits a
// modules_gen.go whose generatedCellModules() returns []composition.CellModule
// (cellmodules/{cell}.Module() calls). This run.go therefore consumes
// []composition.CellModule — NOT the legacy local CellModule interface — so it
// stays type-compatible with that modules_gen.go (the two share package main).
// The cross-module scaffold is skeleton-only: run `gocell generate assembly` to
// produce modules_gen.go / main.go / boundary.yaml, then `go build ./cmd/{{.ID}}`
// compiles. Once business deps are loaded, follow the canonical three-layer
// composition root pattern (see cmd/CLAUDE.md):
//
//  1. Env injection + module factory ({{.HelperName}}Modules)
//  2. composition.New().With(modules...).Build(ctx, shared, runtimeOptsFn) assembles cells + bootstrap.Option
//  3. Three listeners + bootstrap.New(opts...).Run(ctx)
package main

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/framework/runtime/composition"
)

// {{.HelperName}} is the hand-written runtime helper for the {{.ID}}
// assembly. Implement env loading + composition Build before serving real
// cells; the scaffold leaves this as a not-implemented stub so that — after
// `gocell generate assembly --id={{.ID}}` produces modules_gen.go —
// `go build ./cmd/{{.ID}}/...` succeeds while signaling that integration is
// pending.
func {{.HelperName}}(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	modules, err := {{.HelperName}}Modules(assemblyID, assemblyCellIDs)
	if err != nil {
		return err
	}
	_ = modules
	// Scaffold stub: blocks until the context is cancelled (SIGINT/SIGTERM).
	// Replace with composition.New().With(modules...).Build(ctx, shared, runtimeOptsFn)
	// + bootstrap.New(opts...).Run(ctx) (see cmd/CLAUDE.md three-layer pattern)
	// once SharedDeps and listener wiring are ready.
	<-ctx.Done()
	return nil
}

// {{.HelperName}}Modules wraps generatedCellModules() (modules_gen.go) with a
// drift check against assembly.yaml.cells. Mismatch fails-fast and points the
// operator at `gocell generate assembly --id={{.ID}}`. The composition-API form
// returns []composition.CellModule, matching the modules_gen.go that
// `gocell generate assembly` emits for a build.compositionAPI: true assembly.
func {{.HelperName}}Modules(assemblyID string, cellIDs []string) ([]composition.CellModule, error) {
	mods := generatedCellModules()
	if err := assertModuleIDsMatch(assemblyID, cellIDs, mods); err != nil {
		return nil, err
	}
	return mods, nil
}

// assertModuleIDsMatch fails-fast when assembly.yaml.cells (cellIDs) drifts
// from the generated module list. The two should be 1:1 in declaration order;
// any mismatch indicates a missing `gocell generate assembly` run.
func assertModuleIDsMatch(assemblyID string, cellIDs []string, mods []composition.CellModule) error {
	hint := fmt.Sprintf("run `gocell generate assembly --id=%s`", assemblyID)
	if len(cellIDs) != len(mods) {
		return fmt.Errorf(
			"%s: assembly.yaml cells (%d) ↔ modules_gen.go (%d) length mismatch; %s",
			assemblyID, len(cellIDs), len(mods), hint)
	}
	for i, want := range cellIDs {
		if got := mods[i].ID(); got != want {
			return fmt.Errorf(
				"%s: assembly.yaml cells[%d]=%q ↔ modules_gen.go=%q drift; %s",
				assemblyID, i, want, got, hint)
		}
	}
	return nil
}
