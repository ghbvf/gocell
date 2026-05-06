package app

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

const codegenAssemblyKind = "assembly"

// assemblyDriftResult implements CodegenResult for assembly modules_gen.go
// codegen. Generated holds paths written (or would-write in verify mode);
// Drifted holds paths whose on-disk content differs from the generated content.
type assemblyDriftResult struct {
	generated []string
	drifted   []string
}

func (r assemblyDriftResult) GeneratedFiles() []string { return r.generated }
func (r assemblyDriftResult) DriftedFiles() []string   { return r.drifted }

// assemblyCodegenSpec is the codegenSpec[R] wiring for assembly modules_gen.go.
// The Generate function regenerates all assemblies with cells>0; the `only`
// parameter (per-id scoping) is intentionally unused because assembly codegen
// has no per-id filter—all or nothing matches the existing behavior.
// dryRun is also unused: assembly codegen writes inline without a dry-run path.
var assemblyCodegenSpec = codegenSpec[assemblyDriftResult]{
	Kind:            codegenAssemblyKind,
	GenerateUsage:   "gocell generate assembly --id=<assemblyID> | --all",
	AllFlagDesc:     "regenerate modules_gen.go for all assemblies (cells>0)",
	PluralNoun:      "assembly modules_gen.go",
	SourceArtifacts: "assembly.yaml / cell.yaml goStructName",
	Generate:        generateAssemblyModulesGen,
}

// generateAssemblyModulesGen is the Generate func for assemblyCodegenSpec.
// When verify=true it diffs in-memory content against disk; when verify=false
// it writes (or no-ops if byte-equal). The `only` and `dryRun` params are
// ignored—assembly codegen has no per-id scoping and no dry-run mode.
func generateAssemblyModulesGen(root string, project *metadata.ProjectMeta, _, verify bool, _ string) (assemblyDriftResult, error) {
	mod, err := readModule(root)
	if err != nil {
		return assemblyDriftResult{}, fmt.Errorf("cannot read module from go.mod: %w", err)
	}

	ids := make([]string, 0, len(project.Assemblies))
	for id := range project.Assemblies {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	gen := assembly.NewGenerator(project, mod, root)
	var result assemblyDriftResult
	for _, asmID := range ids {
		asm := project.Assemblies[asmID]
		if asm == nil || len(asm.Cells) == 0 {
			// B1 guard: skip nil or empty-cell assemblies — no factory list to register.
			continue
		}
		if err := processOneAssemblyModulesGen(root, gen, asmID, asm, verify, &result); err != nil {
			return assemblyDriftResult{}, err
		}
	}
	return result, nil
}

// processOneAssemblyModulesGen handles modules_gen.go for a single assembly.
// It appends to result.drifted (verify mode) or result.generated (write mode).
func processOneAssemblyModulesGen(
	root string, gen *assembly.Generator,
	asmID string, asm *metadata.AssemblyMeta,
	verify bool, result *assemblyDriftResult,
) error {
	content, err := gen.GenerateModulesGen(asmID)
	if err != nil {
		return fmt.Errorf("regenerate modules_gen %s: %w", asmID, err)
	}

	entrypointRel := asm.Build.Entrypoint
	if entrypointRel == "" {
		entrypointRel = filepath.Join("cmd", asmID, "main.go")
	}
	outPath := filepath.Join(root, filepath.Dir(entrypointRel), "modules_gen.go")
	relPath := filepath.Join(filepath.Dir(entrypointRel), "modules_gen.go")

	res, werr := codegen.Write(codegen.WriteOptions{
		Path:     outPath,
		Content:  content,
		RepoRoot: root,
		Verify:   verify,
	})
	if werr != nil {
		if verify {
			return fmt.Errorf("verify modules_gen %s: %w", asmID, werr)
		}
		return fmt.Errorf("write modules_gen %s: %w", asmID, werr)
	}
	if res.Action == codegen.ActionDrifted {
		result.drifted = append(result.drifted, relPath)
	}
	if !verify {
		result.generated = append(result.generated, relPath)
	}
	return nil
}

// runVerifyCodegenAssembly implements `gocell verify codegen-assembly`.
// Delegates to the shared codegenSpec[R] framework (runCodegenVerify).
func runVerifyCodegenAssembly(args []string) error {
	return runCodegenVerify(assemblyCodegenSpec, args)
}
