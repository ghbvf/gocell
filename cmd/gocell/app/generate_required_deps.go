package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/codegen/requireddepsgen"
)

const requiredDepsGenFileName = "service_required_gen.go"

// requiredDepsKind is the codegen "kind" token for this command, passed to
// writeDriftFixHint / driftErrorTemplate. Extracted to satisfy go:S1192.
const requiredDepsKind = "required-deps"

// generateRequiredDeps implements `gocell generate required-deps`.
//
// Modes:
//
//	<slicePath>  : regenerate one slice's service_required_gen.go
//	--all        : walk cells/**, corecells/**, and examples/** service.go files
//	--verify     : regenerate in-memory and diff against committed files; exit 1 on drift
//	--dry-run    : print planned output to stdout instead of writing
func generateRequiredDeps(args []string) error {
	fs := flag.NewFlagSet("generate required-deps", flag.ContinueOnError)
	all := fs.Bool("all", false, "generate for every slice with service.go")
	dryRun := fs.Bool("dry-run", false, "print would-write file paths without writing")
	verify := fs.Bool("verify", false, "diff against disk, exit non-zero on drift, no write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dryRun && *verify {
		return errors.New("--dry-run (stdout preview) and --verify (CI drift check, no write) are mutually exclusive; pick one")
	}

	pos := fs.Args()
	if len(pos) > 1 {
		return fmt.Errorf("only one slice path allowed; got: %v", pos)
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	if len(pos) == 1 {
		return runRequiredDepsOne(root, pos[0], *dryRun, *verify)
	}
	if !*all {
		return fmt.Errorf("usage: gocell generate required-deps [<slicePath>] [--dry-run | --verify]")
	}
	return runRequiredDepsAll(root, *dryRun, *verify)
}

// runRequiredDepsOne generates for a single slice path.
func runRequiredDepsOne(root, slicePath string, dryRun, verify bool) error {
	// slicePath may be relative; resolve against cwd (which findRoot walked from).
	if !filepath.IsAbs(slicePath) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		slicePath = filepath.Join(cwd, slicePath)
	}

	content, err := requireddepsgen.Generate(slicePath)
	if err != nil {
		return err
	}

	outPath := filepath.Join(slicePath, requiredDepsGenFileName)
	return writeRequiredDepsFile(root, outPath, content, dryRun, verify)
}

// runRequiredDepsAll walks the module root, generates for every discovered
// slice, and reports drift or written files.
func runRequiredDepsAll(root string, dryRun, verify bool) error {
	results, err := requireddepsgen.GenerateAll(root)
	if err != nil {
		return err
	}

	// Sort slice paths for deterministic output.
	paths := make([]string, 0, len(results))
	for p := range results {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var drifted []string
	for _, slicePath := range paths {
		outPath := filepath.Join(slicePath, requiredDepsGenFileName)
		res, err := codegen.Write(codegen.WriteOptions{
			Path:     outPath,
			Content:  results[slicePath],
			RepoRoot: root,
			DryRun:   dryRun,
			Verify:   verify,
		})
		if err != nil {
			return err
		}
		switch res.Action {
		case codegen.ActionWritten:
			fmt.Printf("Generated: %s\n", outPath)
		case codegen.ActionWouldWrite:
			fmt.Printf("Would write: %s\n", outPath)
		case codegen.ActionDrifted:
			fmt.Fprintf(os.Stderr, "drift: %s\n", outPath)
			drifted = append(drifted, outPath)
		}
	}

	if verify && len(drifted) > 0 {
		writeDriftFixHint(requiredDepsKind)
		return fmt.Errorf(driftErrorTemplate, len(drifted), requiredDepsKind)
	}
	return nil
}

// writeRequiredDepsFile delegates to codegen.Write for a single output file.
func writeRequiredDepsFile(root, outPath string, content []byte, dryRun, verify bool) error {
	res, err := codegen.Write(codegen.WriteOptions{
		Path:     outPath,
		Content:  content,
		RepoRoot: root,
		DryRun:   dryRun,
		Verify:   verify,
	})
	if err != nil {
		return err
	}
	switch res.Action {
	case codegen.ActionWritten:
		fmt.Printf("Generated: %s\n", outPath)
	case codegen.ActionWouldWrite:
		fmt.Printf("Would write: %s\n", outPath)
	case codegen.ActionDrifted:
		fmt.Fprintf(os.Stderr, "drift: %s\n", outPath)
		writeDriftFixHint(requiredDepsKind)
		return fmt.Errorf(driftErrorTemplate, 1, requiredDepsKind)
	}
	return nil
}
