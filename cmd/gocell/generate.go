package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// runGenerate implements:
//
//	gocell generate assembly --id=<id> [--module=<module>]
//	gocell generate indexes (placeholder)
//	gocell generate boundaries (placeholder)
func runGenerate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell generate <assembly|indexes|boundaries> [flags]")
	}

	subtype := args[0]
	subArgs := args[1:]

	switch subtype {
	case "assembly":
		return generateAssembly(subArgs)
	case "indexes":
		return fmt.Errorf("not implemented: gocell generate indexes")
	case "boundaries":
		return fmt.Errorf("not implemented: gocell generate boundaries")
	default:
		return fmt.Errorf("unknown generate type: %s (expected assembly, indexes, or boundaries)", subtype)
	}
}

func generateAssembly(args []string) error {
	fs := flag.NewFlagSet("generate assembly", flag.ContinueOnError)
	id := fs.String("id", "", "assembly ID (required)")
	module := fs.String("module", "", "Go module path (default: read from go.mod)")
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

	// Determine module path.
	mod := *module
	if mod == "" {
		var modErr error
		mod, modErr = readModule(root)
		if modErr != nil {
			return fmt.Errorf("cannot read module from go.mod: %w", modErr)
		}
	}

	// Parse metadata.
	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf("metadata parse: %w", err)
	}

	// Generate.
	gen := assembly.NewGenerator(project, mod)

	entrypoint, err := gen.GenerateEntrypoint(*id)
	if err != nil {
		return fmt.Errorf("generate entrypoint: %w", err)
	}

	boundary, err := gen.GenerateBoundary(*id)
	if err != nil {
		return fmt.Errorf("generate boundary: %w", err)
	}

	// Determine entrypoint path from assembly metadata.
	// ref: go-zero goctl — generated file paths driven by configuration
	asm := project.Assemblies[*id]
	entrypointRel := asm.Build.Entrypoint
	if entrypointRel == "" {
		entrypointRel = filepath.Join("cmd", *id, "main.go")
	}
	// The entrypoint path in assembly.yaml is relative to the project root.

	entrypointPath := filepath.Join(root, entrypointRel)
	if !isWithinRoot(root, entrypointPath) {
		return fmt.Errorf("assembly %q build.entrypoint %q: path escapes project root", *id, entrypointRel)
	}
	if err := os.MkdirAll(filepath.Dir(entrypointPath), 0o755); err != nil {
		return fmt.Errorf("create entrypoint dir: %w", err)
	}
	if err := os.WriteFile(entrypointPath, entrypoint, 0o644); err != nil {
		return fmt.Errorf("write entrypoint: %w", err)
	}
	fmt.Printf("Generated: %s\n", entrypointPath)

	// Boundary goes into assemblies/{id}/generated/ (generated artifacts directory).
	generatedDir := filepath.Join(root, "assemblies", *id, "generated")
	if !isWithinRoot(root, generatedDir) {
		return fmt.Errorf("assembly %q: generated dir escapes project root", *id)
	}
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		return fmt.Errorf("create generated dir: %w", err)
	}
	boundaryPath := filepath.Join(generatedDir, "boundary.yaml")
	if err := os.WriteFile(boundaryPath, boundary, 0o644); err != nil {
		return fmt.Errorf("write boundary: %w", err)
	}
	fmt.Printf("Generated: %s\n", boundaryPath)

	return nil
}
