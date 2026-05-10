package scanner_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// writeRel writes content to tmp/rel, creating intermediate directories.
// Distinct from scope_test.go's writeFile (which takes an absolute path) so
// these option-focused tests can keep their fixture trees inline.
func writeRel(t *testing.T, tmp, rel, content string) {
	t.Helper()
	full := filepath.Join(tmp, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// collectRels drives EachContentFile and returns the sorted module-relative
// paths the scope yielded. Any failure inside EachContentFile fatals the
// surrounding test (via the real testing.T) — these tests must construct
// scopes that succeed; the rejection-path tests assert on Scope.Files() error
// directly instead of invoking EachContentFile.
func collectRels(t *testing.T, scope scanner.Scope, suffixes []string) []string {
	t.Helper()
	var rels []string
	scanner.EachContentFile(t, scope, suffixes, func(_ *testing.T, fc scanner.ContentContext) {
		rels = append(rels, fc.Rel)
	})
	sort.Strings(rels)
	return rels
}

func TestMatchRels_FiltersFiles(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeRel(t, tmp, "cells/a/cell.yaml", "a")
	writeRel(t, tmp, "cells/b/cell.yaml", "b")
	writeRel(t, tmp, "cells/a/extras.yaml", "skip")

	scope := scanner.DirsScope(tmp, []string{"cells"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "cell.yaml"
		}),
	)
	got := collectRels(t, scope, []string{".yaml"})
	want := []string{"cells/a/cell.yaml", "cells/b/cell.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMatchRels_AndComposesWithExcludeRels(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeRel(t, tmp, "a/keep.yaml", "k")
	writeRel(t, tmp, "a/excluded.yaml", "x")
	writeRel(t, tmp, "a/no-match.json", "j")

	scope := scanner.DirsScope(tmp, []string{"a"},
		scanner.MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, ".yaml")
		}),
		scanner.ExcludeRels("a/excluded.yaml"),
	)
	got := collectRels(t, scope, []string{".yaml", ".json"})
	want := []string{"a/keep.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMatchRels_MultiplePredicatesChainedAnd(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeRel(t, tmp, "cells/a/cell.yaml", "")
	writeRel(t, tmp, "cells/b/relay.yaml", "")
	writeRel(t, tmp, "cells/c/relay.yaml", "")

	scope := scanner.DirsScope(tmp, []string{"cells"},
		scanner.MatchRels(func(rel string) bool { return strings.HasPrefix(filepath.Base(rel), "relay") }),
		scanner.MatchRels(func(rel string) bool { return strings.HasPrefix(filepath.ToSlash(rel), "cells/b/") }),
	)
	got := collectRels(t, scope, []string{".yaml"})
	want := []string{"cells/b/relay.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMatchRels_NilPredicateIgnored(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeRel(t, tmp, "a.yaml", "")

	scope := scanner.DirsScope(tmp, []string{"."}, scanner.MatchRels(nil))
	got := collectRels(t, scope, []string{".yaml"})
	if len(got) != 1 || got[0] != "a.yaml" {
		t.Errorf("nil predicate should not filter; got %v", got)
	}
}

func TestIncludeTestdata_AllowsTestdataDescent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeRel(t, tmp, "tools/archtest/testdata/foo/case.yaml", "case")
	writeRel(t, tmp, "tools/archtest/testdata/foo/sibling/nested.yaml", "nested")

	scope := scanner.DirsScope(tmp, []string{"tools/archtest/testdata/foo"}, scanner.IncludeTestdata())
	got := collectRels(t, scope, []string{".yaml"})
	want := []string{
		"tools/archtest/testdata/foo/case.yaml",
		"tools/archtest/testdata/foo/sibling/nested.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestIncludeTestdata_RejectedOnModuleScope(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeRel(t, tmp, "tools/archtest/testdata/case.yaml", "")

	scope := scanner.ModuleScope(tmp, scanner.IncludeTestdata())
	if _, err := scope.Files(); err == nil {
		t.Fatal("expected ModuleScope+IncludeTestdata to error; got nil")
	}
}

func TestIncludeTestdata_RejectedOnDirsScopeWithoutTestdataSegment(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeRel(t, tmp, "regular/dir/case.yaml", "")

	scope := scanner.DirsScope(tmp, []string{"regular/dir"}, scanner.IncludeTestdata())
	if _, err := scope.Files(); err == nil {
		t.Fatal("expected DirsScope without testdata segment + IncludeTestdata to error; got nil")
	}
}

func TestIncludeTestdata_DefaultBehaviorStillSkipsTestdata(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeRel(t, tmp, "real/data.yaml", "real")
	writeRel(t, tmp, "real/testdata/skip.yaml", "skip")

	scope := scanner.DirsScope(tmp, []string{"real"})
	got := collectRels(t, scope, []string{".yaml"})
	want := []string{"real/data.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("default: got %v, want %v (testdata must be skipped without IncludeTestdata)", got, want)
	}
}
