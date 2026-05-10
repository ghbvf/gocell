// scaffold_assembly.go implements `gocell scaffold assembly` (K#09).
//
// Produces an assembly skeleton: assemblies/{id}/assembly.yaml +
// cmd/{id}/run.go + cmd/{id}/app.go via kernel/assembly.Generator.Scaffold.
// Unless --skip-generate is set, also auto-invokes the K#10 codegen path
// (modules_gen.go + main.go + boundary.yaml) so `go build ./cmd/{id}/...`
// succeeds immediately after scaffold.
package app

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

// scaffoldAssembly is the subcommand entry for `gocell scaffold assembly`.
// Flag set:
//
//	--id=<assemblyID>           required
//	--cells=<a,b,c>             required (comma-separated existing cells)
//	--team=<team>               required
//	--role=<role>               required
//	--deploy=<k8s|compose|binary> default k8s — k8s is omitted from yaml
//	--dry-run                   render only, no writes
//	--skip-generate             skip auto-invocation of assembly codegen
func scaffoldAssembly(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold assembly", flag.ContinueOnError)
	id := fs.String("id", "", "assembly ID (required)")
	cells := fs.String("cells", "", "comma-separated cell IDs (required, must already exist)")
	team := fs.String("team", "", "owner team (required)")
	role := fs.String("role", "", "owner role, e.g. maintainer (required)")
	deploy := fs.String("deploy", "k8s", "deployment template: one of [k8s compose binary]")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	skipGenerate := fs.Bool(skipGenerateFlag, false, "skip auto-invocation of assembly codegen (modules_gen.go / main.go / boundary.yaml)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if *cells == "" {
		return fmt.Errorf("--cells is required")
	}
	if *team == "" {
		return fmt.Errorf("--team is required")
	}
	if *role == "" {
		return fmt.Errorf("--role is required")
	}

	cellList := splitAndTrim(*cells, ",")
	if len(cellList) == 0 {
		return fmt.Errorf("--cells must list at least one cell")
	}

	mod, err := readModule(root)
	if err != nil {
		return fmt.Errorf("scaffold assembly: read module path: %w", err)
	}

	// Parse the project metadata so the generator can validate cell
	// existence (and so auto-generate has the parsed registries).
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return fmt.Errorf("scaffold assembly: parse project: %w", err)
	}

	gen := assembly.NewGenerator(project, mod, root)
	if err := gen.Scaffold(assembly.AssemblyScaffoldSpec{
		ID:        *id,
		Cells:     cellList,
		OwnerTeam: *team,
		OwnerRole: *role,
		Deploy:    *deploy,
		DryRun:    *dryRun,
	}); err != nil {
		return err
	}

	if *dryRun {
		yamlRel := filepath.Join("assemblies", *id, "assembly.yaml")
		runRel := filepath.Join("cmd", *id, "run.go")
		appRel := filepath.Join("cmd", *id, "app.go")
		fmt.Printf("(dry-run) Would create %s\n", filepath.ToSlash(yamlRel))
		fmt.Printf("(dry-run) Would create %s\n", filepath.ToSlash(runRel))
		fmt.Printf("(dry-run) Would create %s\n", filepath.ToSlash(appRel))
		return nil
	}

	reportScaffold(scaffoldReport{
		Kind:   "assembly",
		ID:     *id,
		Target: filepath.Join("assemblies", *id),
	})

	if *skipGenerate {
		fmt.Printf("scaffold assembly: skipped auto-generate (--skip-generate). "+
			"Run `gocell generate assembly --id=%s` to materialize modules_gen.go / main.go / boundary.yaml.\n",
			*id)
		return nil
	}

	// Auto-generate K#10 derived artifacts. The just-written assembly.yaml
	// must be re-parsed because the in-memory project does not include it.
	freshProject, err := metadata.NewParser(root).Parse()
	if err != nil {
		return fmt.Errorf("scaffold assembly: re-parse project for codegen: %w", err)
	}
	if err := autoGenerateAssemblyArtifacts(root, mod, freshProject, *id); err != nil {
		return fmt.Errorf("scaffold assembly: auto-generate: %w", err)
	}
	return nil
}

// autoGenerateAssemblyArtifacts runs the K#10 derived-file generators for
// a single assembly: modules_gen.go, main.go, boundary.yaml.
func autoGenerateAssemblyArtifacts(root, mod string, project *metadata.ProjectMeta, assemblyID string) error {
	gen := assembly.NewGenerator(project, mod, root)
	asm := project.Assemblies[assemblyID]
	if asm == nil {
		return fmt.Errorf("assembly %q not found in re-parsed project", assemblyID)
	}

	// modules_gen.go under cmd/{id}/.
	modulesContent, err := gen.GenerateModulesGen(assemblyID)
	if err != nil {
		return fmt.Errorf("generate modules_gen: %w", err)
	}
	modulesPath := filepath.Join(root, "cmd", assemblyID, "modules_gen.go")
	if _, err := codegen.Write(codegen.WriteOptions{Path: modulesPath, Content: modulesContent, RepoRoot: root}); err != nil {
		return fmt.Errorf("write modules_gen: %w", err)
	}

	// main.go entrypoint under cmd/{id}/.
	mainContent, err := gen.GenerateEntrypoint(assemblyID)
	if err != nil {
		return fmt.Errorf("generate main.go: %w", err)
	}
	entrypointRel := asm.Build.Entrypoint
	if entrypointRel == "" {
		entrypointRel = filepath.Join("cmd", assemblyID, "main.go")
	}
	mainPath := filepath.Join(root, entrypointRel)
	if _, err := codegen.Write(codegen.WriteOptions{Path: mainPath, Content: mainContent, RepoRoot: root}); err != nil {
		return fmt.Errorf("write main.go: %w", err)
	}

	// boundary.yaml under assemblies/{id}/generated/.
	boundaryContent, err := gen.GenerateBoundary(assemblyID)
	if err != nil {
		return fmt.Errorf("generate boundary.yaml: %w", err)
	}
	boundaryPath := filepath.Join(root, "assemblies", assemblyID, "generated", "boundary.yaml")
	if _, err := codegen.Write(codegen.WriteOptions{Path: boundaryPath, Content: boundaryContent, RepoRoot: root}); err != nil {
		return fmt.Errorf("write boundary.yaml: %w", err)
	}
	return nil
}

// splitAndTrim splits s by sep and trims whitespace from each segment;
// empty segments are dropped.
func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
