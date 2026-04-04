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
		fmt.Println("generate indexes: not implemented yet")
		return nil
	case "boundaries":
		fmt.Println("generate boundaries: not implemented yet")
		return nil
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

	// Write files.
	assemblyDir := filepath.Join(root, "assemblies", *id)
	if err := os.MkdirAll(assemblyDir, 0o755); err != nil {
		return fmt.Errorf("create assembly dir: %w", err)
	}

	entrypointPath := filepath.Join(assemblyDir, "main.go")
	if err := os.WriteFile(entrypointPath, entrypoint, 0o644); err != nil {
		return fmt.Errorf("write entrypoint: %w", err)
	}
	fmt.Printf("Generated: %s\n", entrypointPath)

	boundaryPath := filepath.Join(assemblyDir, "boundary.yaml")
	if err := os.WriteFile(boundaryPath, boundary, 0o644); err != nil {
		return fmt.Errorf("write boundary: %w", err)
	}
	fmt.Printf("Generated: %s\n", boundaryPath)

	return nil
}
