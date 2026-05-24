package app

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/metricschema"
)

// generateSubcommands is the single source of truth for `gocell generate`
// (see subcommand.go / CLI-UNIMPL-HIDE-01). assembly/catalog/cell/contract
// have no cancelable downstream (codegen is metadata + file IO), so their
// closures discard ctx; metrics-schema threads it into metricschema.Build.
var generateSubcommands = []subcommand[func(ctx context.Context, args []string) error]{
	{
		name: "assembly",
		help: []string{
			"Generate the assembly entrypoint <derived>/main.go,",
			"<derived>/generated/boundary.yaml, and",
			"<derived>/modules_gen.go (the cell→Module factory).",
			"Paths derive from the assembly.yaml location:",
			"  assemblies/<id>/assembly.yaml → cmd/<id>/, assemblies/<id>/generated/",
			"  examples/<id>/assembly.yaml  → examples/<id>/, examples/<id>/generated/",
			"Generated files are owned by gocell. Hand-written",
			"helpers live in <derived>/run.go etc., but",
			"<derived>/main.go and <derived>/modules_gen.go must",
			"carry the gocell generated header or generation",
			"aborts to protect your edits.",
			"--id=<assemblyID> | --all [--module=<module>]",
		},
		run: func(_ context.Context, a []string) error { return generateAssembly(a) },
	},
	{
		name: "metrics-schema",
		help: []string{
			"Generate <derived>/generated/metrics-schema.yaml by walking",
			"the assembly's reachable packages with go/types.",
			"--id=<assemblyID> | --all",
		},
		run: generateMetricsSchema,
	},
	{
		name: "catalog",
		help: []string{
			"Render the project catalog Go source from metadata.",
			"--out=<path> --package=<pkg>",
		},
		run: func(_ context.Context, a []string) error { return generateCatalog(a) },
	},
	{
		name: "cell",
		help: []string{
			"Render cell_gen.go and slice_gen.go from cell.yaml /",
			"slice.yaml. Default: all opted-in cells (goStructName set).",
			"Optional: [<cellID>] scopes to a single cell.",
			"--verify reports drift without writing; --dry-run prints",
			"would-write file paths without writing.",
			"CI: commit cell_gen.go and run with --verify to detect stale artifacts.",
		},
		run: func(_ context.Context, a []string) error { return generateCell(a) },
	},
	{
		name: "contract",
		help: []string{
			"Render generated/contracts/**/*_gen.go from contract.yaml",
			"+ JSON schemas. <contractID> | --all [--dry-run | --verify].",
			"--verify reports drift without writing; --dry-run prints",
			"would-write paths without writing.",
			"Default: codegen on; set codegen: false to opt out.",
			"CI: commit *_gen.go files and run with --verify.",
		},
		run: func(_ context.Context, a []string) error { return generateContract(a) },
	},
	{
		name: "required-deps",
		help: []string{
			"Generate service_required_gen.go for each slice that has",
			"gocell:\"required\" struct field tags on its Service struct.",
			"[<slicePath>] | --all [--dry-run | --verify].",
			"--verify reports drift without writing; --dry-run prints",
			"would-write paths without writing.",
			"CI: commit service_required_gen.go and run with --verify.",
		},
		run: func(_ context.Context, a []string) error { return generateRequiredDeps(a) },
	},
	{
		name: "shared-schema",
		help: []string{
			"Derive byte-identical mirror copies of",
			"contracts/shared/errors/error-response-v1.schema.json",
			"to every destination declared in sharedschema.Mirrors.",
			"--all (required) [--dry-run].",
			"CI: commit mirrors and run `gocell verify codegen-shared-schema`.",
		},
		run: func(_ context.Context, a []string) error { return generateSharedSchema(a) },
	},
}

// runGenerate dispatches `gocell generate <type>` through the
// generateSubcommands registry. ctx originates from the signal-aware
// context wired in main.go and reaches metricschema.Build for the
// metrics-schema type.
func runGenerate(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell generate <%s> [flags]",
			strings.Join(subNames(generateSubcommands), "|"))
	}
	if isHelpFlag(args[0]) {
		return renderSubHelp("generate", generateSubcommands,
			"Generated artifacts must be committed in HEAD; gocell verify generated",
			"rejects stale or staged-only files.")
	}
	run, ok := findSub(generateSubcommands, args[0])
	if !ok {
		return fmt.Errorf("unknown generate type: %s (expected %s)",
			args[0], strings.Join(subNames(generateSubcommands), ", "))
	}
	return run(ctx, args[1:])
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
// Output is sorted by id so iteration order is stable across runs (Go map
// iteration is randomized, which would otherwise leak into stdout / generated
// path lists and break golden-file comparisons).
func assemblyIDsToGenerate(project *metadata.ProjectMeta, id string, all bool) []string {
	if !all {
		return []string{id}
	}
	ids := make([]string, 0, len(project.Assemblies))
	for asmID := range project.Assemblies {
		ids = append(ids, asmID)
	}
	sort.Strings(ids)
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
	// asm.Build.Entrypoint is guaranteed non-empty by deriveAssembly; if it
	// is somehow empty here the parser has a bug — fail loudly rather than
	// silently routing to cmd/{id}/ which would be wrong for examples/ assemblies.
	asm := project.Assemblies[id]
	if asm.Build.Entrypoint == "" {
		return fmt.Errorf("assembly %q: asm.Build.Entrypoint not derived; parser bug", id)
	}
	entrypointRel := asm.Build.Entrypoint
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

	// Boundary goes into the assembly's derived generated/ directory.
	// For examples/ assemblies this is examples/{id}/generated/; for all
	// others it is assemblies/{id}/generated/ (AssemblyGeneratedDir single
	// source of truth, mirrors deriveAssembly entrypoint logic).
	generatedDir := metadata.AssemblyGeneratedDir(asm)
	boundaryPath := filepath.Join(root, filepath.FromSlash(generatedDir), "boundary.yaml")
	return writeGeneratedFile(root, boundaryPath, boundary,
		fmt.Sprintf("assembly %q generated dir", id))
}

// generateMetricsSchema implements:
//
//	gocell generate metrics-schema --id=<assemblyID>
//	gocell generate metrics-schema --all
//
// It loads the assembly entrypoint with go/packages, walks the reachable
// project packages with type information, serializes the result to
// <derived>/generated/metrics-schema.yaml, and prints the output path.
// Run this command locally and commit the result whenever a metric name, label
// set, bucket list, or bucket source changes.
func generateMetricsSchema(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("generate metrics-schema", flag.ContinueOnError)
	id := fs.String("id", "", "assembly ID (mutually exclusive with --all)")
	all := fs.Bool("all", false, "generate for every assembly")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" && !*all {
		return fmt.Errorf("usage: gocell generate metrics-schema --id=<assemblyID> | --all")
	}
	if *id != "" && *all {
		return fmt.Errorf("--id and --all are mutually exclusive")
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

	ids := assemblyIDsToGenerate(project, *id, *all)
	for _, asmID := range ids {
		if err := generateOneMetricsSchema(ctx, root, project, asmID); err != nil {
			return err
		}
	}
	return nil
}

// generateOneMetricsSchema generates metrics-schema.yaml for a single assembly.
func generateOneMetricsSchema(ctx context.Context, root string, project *metadata.ProjectMeta, id string) error {
	// ctx is the signal-aware context plumbed from main.go through
	// Dispatch → runGenerate; metricschema.Build walks packages with
	// go/types and honors cancellation.
	schema, err := metricschema.Build(ctx, root, project, id)
	if err != nil {
		return fmt.Errorf("scan metrics: %w", err)
	}

	content, err := metricschema.Marshal(schema)
	if err != nil {
		return fmt.Errorf("serialize metrics-schema: %w", err)
	}

	// metrics-schema.yaml lives in the same generated/ directory as boundary.yaml.
	// AssemblyGeneratedDir is the single source of truth: examples/ assemblies
	// go to examples/{id}/generated/, others to assemblies/{id}/generated/.
	asm := project.Assemblies[id]
	generatedDir := metadata.AssemblyGeneratedDir(asm)
	outPath := filepath.Join(root, filepath.FromSlash(generatedDir), "metrics-schema.yaml")
	return writeGeneratedFile(root, outPath, content,
		fmt.Sprintf("assembly %q metrics-schema", id))
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
