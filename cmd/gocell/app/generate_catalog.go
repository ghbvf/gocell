package app

// generate_catalog.go — implements `gocell generate catalog`, which emits a
// build-time Go file containing a typed *kerneldepgraph.Graph literal.
//
// The generated file is consumed by cmd/corebundle via generatedPackageGraph
// so that the HTTP handler never needs a runtime goroutine to load the graph.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/tools/depgraph"
	"github.com/ghbvf/gocell/tools/generatedcatalog"
)

// generateCatalog implements:
//
//	gocell generate catalog --out=<path> --package=<pkg> [--root=<dir>]
func generateCatalog(args []string) error {
	fs := flag.NewFlagSet("generate catalog", flag.ContinueOnError)
	out := fs.String("out", "", "output .go file path (required)")
	pkg := fs.String("package", "", "Go package declaration name (required)")
	root := fs.String("root", "", "project root directory; empty triggers go.mod auto-detection")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	if *pkg == "" {
		return fmt.Errorf("--package is required")
	}

	rootDir, err := resolveRoot(*root)
	if err != nil {
		return err
	}

	modulePath, err := readModule(rootDir)
	if err != nil {
		return fmt.Errorf("cannot read module from go.mod: %w", err)
	}

	// Load the package dep graph synchronously (CLI context — acceptable latency).
	g, err := depgraph.Load(depgraph.LoadOptions{Dir: rootDir}, "./...")
	if err != nil {
		return fmt.Errorf("generate catalog: depgraph load: %w", err)
	}

	// Emit the Go source file.
	src, err := generatedcatalog.EmitFile(*pkg, modulePath, g)
	if err != nil {
		return fmt.Errorf("generate catalog: emit: %w", err)
	}

	// Ensure parent dir exists.
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(*out)), 0o750); err != nil {
		return fmt.Errorf("generate catalog: mkdir: %w", err)
	}

	if err := os.WriteFile(filepath.Clean(*out), src, 0o600); err != nil {
		return fmt.Errorf("generate catalog: write: %w", err)
	}
	fmt.Printf("Generated: %s\n", *out)
	return nil
}
