package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ContentContext is the non-Go counterpart of [FileContext]: raw bytes only,
// no AST and no token.FileSet. Use it for YAML / JSON / Markdown / SQL or any
// other format the scanner framework should funnel.
type ContentContext struct {
	// AbsPath is the absolute path to the file.
	AbsPath string
	// Rel is the module-relative slash path (e.g. "cells/auth/cell.yaml").
	Rel string
	// Bytes is the file's raw content. Caller decodes with the lib of choice.
	Bytes []byte
}

// EachContentFile iterates over every file in scope whose path ends in any
// of suffixes (case-sensitive, must include the dot — e.g. ".yaml"). Read
// errors fail-loud via t.Fatalf. fn is invoked for each successfully read
// file with the file's bytes; calling t.Errorf inside fn does not stop
// iteration (collect-all-violations semantics, mirroring [EachFile]).
//
// Suffixes must be non-empty and each must start with ".". This is the only
// way archtest tests should iterate non-Go files; raw os.ReadDir / fs.WalkDir
// in tools/archtest/*_test.go is forbidden by SCANNER-FRAMEWORK-USAGE-01.
func EachContentFile(t *testing.T, s Scope, suffixes []string, fn func(*testing.T, ContentContext)) {
	t.Helper()
	if len(suffixes) == 0 {
		t.Fatalf("scanner.EachContentFile: suffixes must be non-empty")
	}
	for _, suffix := range suffixes {
		if !strings.HasPrefix(suffix, ".") {
			t.Fatalf("scanner.EachContentFile: suffix %q must start with '.'", suffix)
		}
	}
	files, err := s.contentFiles(suffixes)
	if err != nil {
		t.Fatalf("scanner.EachContentFile: %v", err)
	}
	for _, absPath := range files {
		rel, relErr := filepath.Rel(s.modRoot, absPath)
		if relErr != nil {
			t.Fatalf("scanner.EachContentFile: rel-failed: %v", relErr)
		}
		relSlash := filepath.ToSlash(rel)
		// #nosec G304 -- absPath is derived from a checked-in module subtree
		// already filtered through scope.collectFile (path-segment escape
		// guard + selfProtect + ExcludeRels + MatchRels). archtest reads
		// repo-resident files under module root; treating discovered paths as
		// "user input" would force every archtest read through an arbitrary
		// allowlist for no security gain.
		bytes, readErr := os.ReadFile(absPath)
		if readErr != nil {
			t.Fatalf("scanner.EachContentFile: read %s: %v", relSlash, readErr)
		}
		fn(t, ContentContext{
			AbsPath: absPath,
			Rel:     relSlash,
			Bytes:   bytes,
		})
	}
}
