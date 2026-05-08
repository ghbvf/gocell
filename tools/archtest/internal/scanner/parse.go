package scanner

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// FileContext holds the parsed AST and metadata for a single Go source file.
type FileContext struct {
	// AbsPath is the absolute path to the file.
	AbsPath string
	// Rel is the path relative to the scope's module root.
	Rel string
	// Fset is the file set used during parsing.
	Fset *token.FileSet
	// File is the parsed AST.
	File *ast.File
}

// eachFile is the internal, pure-function entry point for iterating over all
// files in scope. It calls fn for each successfully parsed file. Any parse error
// or fn error is returned immediately (fail-closed).
func eachFile(s Scope, mode parser.Mode, fn func(FileContext) error) error {
	files, err := s.Files()
	if err != nil {
		return err
	}
	for _, absPath := range files {
		rel, relErr := filepath.Rel(s.modRoot, absPath)
		if relErr != nil {
			// rel computation failed: use a clearly-labeled fallback so
			// absolute paths do not appear in CI logs unexpectedly.
			return fmt.Errorf("rel-failed: %w", relErr)
		}
		relSlash := filepath.ToSlash(rel)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, absPath, nil, mode)
		if err != nil {
			return fmt.Errorf("parse %s: %w", relSlash, err)
		}
		fc := FileContext{
			AbsPath: absPath,
			Rel:     relSlash,
			Fset:    fset,
			File:    f,
		}
		if err := fn(fc); err != nil {
			return err
		}
	}
	return nil
}

// EachFile iterates over every file in scope, parsing each with the given mode.
// Any parse error causes t.Fatalf immediately, stopping the entire test
// (fail-loud by construction; no silent fallback). fn is invoked for each
// successfully parsed file; calling t.Errorf inside fn does not stop iteration
// (collect-all-violations semantics — the loop continues to accumulate further
// findings before the test ultimately fails).
func EachFile(t *testing.T, s Scope, mode parser.Mode, fn func(*testing.T, FileContext)) {
	t.Helper()
	if err := eachFile(s, mode, func(fc FileContext) error {
		fn(t, fc)
		return nil
	}); err != nil {
		t.Fatalf("scanner.EachFile: %v", err)
	}
}
