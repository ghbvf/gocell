// run.go is the handwritten runtime half behind the generated assembly
// entrypoint for {{.ID}}. The generated main.go owns the assembly ID and
// cell order; this file owns environment loading and runtime option wiring.
//
// K#09 SCAFFOLD-ONE-CMD scaffolded skeleton — replace TODO stubs with real
// dependency loading. Once business code is wired, run.go must follow the
// canonical three-layer composition root pattern (see cmd/CLAUDE.md):
//
//  1. Env injection + module factory ({{.HelperName}}Modules)
//  2. BuildApp assembles cells + bootstrap.Option
//  3. Three listeners + bootstrap.New(opts...).Run(ctx)
package main

import (
	"context"
	"fmt"
)

// {{.HelperName}} is the hand-written runtime helper for the {{.ID}}
// assembly. Implement env loading + bootstrap wiring before invoking real
// cells; the K#09 scaffold leaves this as a not-implemented stub so that
// `go build ./cmd/{{.ID}}/...` succeeds while signaling that integration is
// pending.
func {{.HelperName}}(_ context.Context, assemblyID string, assemblyCellIDs []string) error {
	modules, err := {{.HelperName}}Modules(assemblyID, assemblyCellIDs)
	if err != nil {
		return err
	}
	_ = modules
	// TODO: implement composition root.
	//
	//   shared, err := LoadSharedDepsFromEnv(ctx)
	//   if err != nil { return err }
	//   cells, cellOpts, err := BuildApp(ctx, shared, modules...)
	//   if err != nil { return err }
	//   asm, err := buildAssembly(shared.PromStack, assemblyID, ..., cells...)
	//   if err != nil { return fmt.Errorf("build assembly: %w", err) }
	//   opts := defaultRuntimeOptions(shared, asm, ...)
	//   opts = append(opts, cellOpts...)
	//   return bootstrap.New(opts...).Run(ctx)
	return fmt.Errorf("{{.HelperName}}: not implemented; replace stub with composition root")
}

// {{.HelperName}}Modules wraps generatedCellModules() (modules_gen.go) with a
// drift check against assembly.yaml.cells. Mismatch fails-fast and points
// the operator at `gocell generate assembly --id={{.ID}}`.
func {{.HelperName}}Modules(assemblyID string, cellIDs []string) ([]CellModule, error) {
	mods := generatedCellModules()
	if err := assertModuleIDsMatch(assemblyID, cellIDs, mods); err != nil {
		return nil, err
	}
	return mods, nil
}

// assertModuleIDsMatch fails-fast when assembly.yaml.cells (cellIDs) drifts
// from the generated module list. The two should be 1:1 in declaration order;
// any mismatch indicates a missing `gocell generate assembly` run.
func assertModuleIDsMatch(assemblyID string, cellIDs []string, mods []CellModule) error {
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

// CellModule is the K#10 modules_gen.go interface contract: each generated
// factory returns a value implementing ID(). The {{.ID}} scaffold leaves
// this declaration here so generated modules_gen.go compiles immediately;
// replace with the real CellModule type from your composition root once
// SharedDeps / Provide are wired.
type CellModule interface {
	ID() string
}

// Stub *Module types per cell — declared so modules_gen.go compiles. Each
// concrete CellModule receiver returns the cell ID; replace each with a
// real composition root struct (see cmd/corebundle/access_module.go for a
// production example) carrying construction + Provide() once the assembly
// is wired to real dependencies.
{{range .CellModules}}
// {{.Name}} is the K#09 scaffold stub; replace with the real composition root.
type {{.Name}} struct{}

// ID is the stub identifier — derived from the matching cell.yaml id.
func ({{.Name}}) ID() string { return {{printf "%q" .ID}} }
{{end}}
