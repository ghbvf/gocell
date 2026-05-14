package archtest

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ContentContext is the non-Go counterpart of [FileContext] for YAML / JSON /
// Markdown / SQL etc. Plain bytes only — no AST, no FileSet.
type ContentContext = scanner.ContentContext

// LoadContentFiles is the pure testing-free reader: validates suffixes, walks
// the scope, returns a slice of [ContentContext]. Direct callers should prefer
// [EachContentFile] for fail-loud test ergonomics.
//
// Wrapper around [scanner.LoadContentFiles].
func LoadContentFiles(s Scope, suffixes []string) ([]ContentContext, error) {
	return scanner.LoadContentFiles(s, suffixes)
}

// EachContentFile iterates every file in scope whose path ends in any of
// suffixes (case-sensitive, must include the dot). Validation / walk / read
// errors fail-loud via t.Fatalf; fn is invoked per file with the bytes.
// Calling t.Errorf inside fn does not stop iteration.
//
// Wrapper around [scanner.EachContentFile].
func EachContentFile(t *testing.T, s Scope, suffixes []string, fn func(*testing.T, ContentContext)) {
	t.Helper()
	scanner.EachContentFile(t, s, suffixes, fn)
}
