package app

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/metricschema"
)

// runGenerate implements:
//
//	gocell generate assembly --id=<id> [--module=<module>]
//	gocell generate catalog --out=<path> --package=<pkg>
//	gocell generate metrics-schema --id=<id>
//	gocell generate cell [<cellID>] [--dry-run | --verify]
//	gocell generate contract [<contractID>] [--dry-run | --verify]
//	gocell generate indexes (placeholder)
func runGenerate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell generate <assembly|catalog|metrics-schema|cell|contract|indexes> [flags]")
	}
	if isHelpFlag(args[0]) {
		return printGenerateHelp()
	}

	subtype := args[0]
	subArgs := args[1:]

	switch subtype {
	case "assembly":
		return generateAssembly(subArgs)
	case "metrics-schema":
		return generateMetricsSchema(subArgs)
	case "catalog":
		return generateCatalog(subArgs)
	case "cell":
		return generateCell(subArgs)
	case "contract":
		return generateContract(subArgs)
	case "indexes":
		return fmt.Errorf("not implemented: gocell generate indexes")
	default:
		return fmt.Errorf("unknown generate type: %s (expected assembly, catalog, metrics-schema, cell, contract, or indexes)", subtype)
	}
}

func generateAssembly(args []string) error {
	fs := flag.NewFlagSet("generate assembly", flag.ContinueOnError)
	id := fs.String("id", "", "assembly ID (mutually exclusive with --all)")
	all := fs.Bool("all", false, "generate for every assembly")
	module := fs.String("module", "", "Go module path (default: read from go.mod)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" && !*all {
		return fmt.Errorf("usage: gocell generate assembly --id=<assemblyID> | --all [--module=<module>]")
	}
	if *id != "" && *all {
		return fmt.Errorf("--id and --all are mutually exclusive")
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	mod, err := resolveModule(root, *module)
	if err != nil {
		return err
	}
	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf("metadata parse: %w", err)
	}

	ids := assemblyIDsToGenerate(project, *id, *all)
	for _, asmID := range ids {
		if err := generateOneAssembly(root, project, mod, asmID); err != nil {
			return err
		}
	}
	return nil
}

// resolveModule returns the module path from the flag value or go.mod.
func resolveModule(root, flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	mod, err := readModule(root)
	if err != nil {
		return "", fmt.Errorf("cannot read module from go.mod: %w", err)
	}
	return mod, nil
}

// assemblyIDsToGenerate returns the list of assembly IDs to generate. When
// all=true every key in project.Assemblies is included; otherwise only id.
func assemblyIDsToGenerate(project *metadata.ProjectMeta, id string, all bool) []string {
	if !all {
		return []string{id}
	}
	ids := make([]string, 0, len(project.Assemblies))
	for asmID := range project.Assemblies {
		ids = append(ids, asmID)
	}
	return ids
}

func generateOneAssembly(root string, project *metadata.ProjectMeta, mod, id string) error {
	// Generate.
	gen := assembly.NewGenerator(project, mod, root)

	entrypoint, err := gen.GenerateEntrypoint(id)
	if err != nil {
		return fmt.Errorf("generate entrypoint: %w", err)
	}
	// ref: go-zero goctl — generated file paths driven by configuration
	asm := project.Assemblies[id]
	entrypointRel := asm.Build.Entrypoint
	if entrypointRel == "" {
		entrypointRel = filepath.Join("cmd", id, "main.go")
	}
	entrypointPath := filepath.Join(root, entrypointRel)
	if err := writeGeneratedFile(root, entrypointPath, entrypoint,
		fmt.Sprintf("assembly %q build.entrypoint %q", id, entrypointRel)); err != nil {
		return err
	}

	// B1 guard: only generate modules_gen.go when the assembly has cells.
	if len(asm.Cells) > 0 {
		modulesContent, err := gen.GenerateModulesGen(id)
		if err != nil {
			return fmt.Errorf("generate modules_gen: %w", err)
		}
		modulesPath := filepath.Join(filepath.Dir(entrypointPath), "modules_gen.go")
		if err := writeGeneratedFile(root, modulesPath, modulesContent,
			fmt.Sprintf("assembly %q modules_gen", id)); err != nil {
			return err
		}
	}

	boundary, err := gen.GenerateBoundary(id)
	if err != nil {
		return fmt.Errorf("generate boundary: %w", err)
	}

	// Boundary goes into assemblies/{id}/generated/.
	boundaryPath := filepath.Join(root, "assemblies", id, "generated", "boundary.yaml")
	return writeGeneratedFile(root, boundaryPath, boundary,
		fmt.Sprintf("assembly %q generated dir", id))
}

// generateMetricsSchema implements:
//
//	gocell generate metrics-schema --id=<assemblyID>
//
// It loads the assembly entrypoint with go/packages, walks the reachable
// project packages with type information, serializes the result to
// assemblies/<id>/generated/metrics-schema.yaml, and prints the output path.
// Run this command locally and commit the result whenever a metric name, label
// set, bucket list, or bucket source changes.
func generateMetricsSchema(args []string) error {
	fs := flag.NewFlagSet("generate metrics-schema", flag.ContinueOnError)
	id := fs.String("id", "", "assembly ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--id is required")
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf("metadata parse: %w", err)
	}

	// Boundary: the gocell sub-command dispatcher passes args, not ctx, so
	// metricschema.Build receives Background here. Same pattern as the
	// validate sub-command; replacing both at once requires plumbing a
	// signal-aware ctx through the dispatcher.
	schema, err := metricschema.Build(context.Background(), root, project, *id)
	if err != nil {
		return fmt.Errorf("scan metrics: %w", err)
	}

	content, err := metricschema.Marshal(schema)
	if err != nil {
		return fmt.Errorf("serialize metrics-schema: %w", err)
	}

	outPath := filepath.Join(root, "assemblies", *id, "generated", "metrics-schema.yaml")
	return writeGeneratedFile(root, outPath, content,
		fmt.Sprintf("assembly %q metrics-schema", *id))
}

// writeGeneratedFile is a thin wrapper over tools/codegen.Write that
// preserves the legacy "Generated: <path>" stdout output and prefixes
// errors with a caller label. New codegen subcommands should call
// tools/codegen.Write directly; this shim exists so generate {assembly,
// metrics-schema} retain their pre-codegen-framework error messages.
func writeGeneratedFile(root, outPath string, content []byte, label string) error {
	res, err := codegen.Write(codegen.WriteOptions{
		Path:     outPath,
		Content:  content,
		RepoRoot: root,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if res.Action == codegen.ActionWritten {
		fmt.Printf("Generated: %s\n", outPath)
	}
	return nil
}
