package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// writeFile creates path under tmp (creating intermediate directories) and
// writes content. Test fails on any error.
func writeFile(t *testing.T, tmp, path, content string) {
	t.Helper()
	full := filepath.Join(tmp, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestEachContentFile_BasicSuffixMatch(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeFile(t, tmp, "a.yaml", "k1: v1")
	writeFile(t, tmp, "b.yml", "k2: v2")
	writeFile(t, tmp, "nested/c.yaml", "k3: v3")
	writeFile(t, tmp, "ignored.json", "{}")

	scope := DirsScope(tmp, []string{"."})
	var got []string
	EachContentFile(t, scope, []string{".yaml", ".yml"}, func(t *testing.T, fc ContentContext) {
		got = append(got, fc.Rel)
	})
	sort.Strings(got)
	want := []string{"a.yaml", "b.yml", "nested/c.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEachContentFile_NoMatchReturnsNothing(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeFile(t, tmp, "doc.md", "# hello")

	scope := DirsScope(tmp, []string{"."})
	var hits int
	EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc ContentContext) {
		hits++
	})
	if hits != 0 {
		t.Errorf("got %d hits, want 0", hits)
	}
}

func TestEachContentFile_DeliversBytes(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeFile(t, tmp, "data.json", `{"foo":"bar"}`)

	scope := DirsScope(tmp, []string{"."})
	var got string
	EachContentFile(t, scope, []string{".json"}, func(t *testing.T, fc ContentContext) {
		got = string(fc.Bytes)
	})
	if got != `{"foo":"bar"}` {
		t.Errorf("ContentContext.Bytes = %q, want %q", got, `{"foo":"bar"}`)
	}
}

func TestEachContentFile_HonorsDefaultSkipDirs(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeFile(t, tmp, "real/data.yaml", "real")
	writeFile(t, tmp, "testdata/skip.yaml", "skip")
	writeFile(t, tmp, "vendor/skip.yaml", "skip")
	writeFile(t, tmp, "generated/skip.yaml", "skip")
	writeFile(t, tmp, ".git/skip.yaml", "skip")

	scope := DirsScope(tmp, []string{"."})
	var got []string
	EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc ContentContext) {
		got = append(got, fc.Rel)
	})
	if len(got) != 1 || got[0] != "real/data.yaml" {
		t.Errorf("expected only real/data.yaml (default skipDirs honored); got %v", got)
	}
}

// EachContentFile's t.Fatalf-on-bad-input branches (empty suffixes, missing
// leading dot, zero-value scope) are not unit-tested. t.Fatalf calls
// runtime.Goexit which cannot be intercepted via recover, and t.Run does not
// isolate Goexit between parent and child tests (subtest failure auto-fails
// parent). Adding a mock testing.T would require changing the signature from
// *testing.T to testing.TB, expanding API surface for trivial validations
// that are 4 lines of obvious code each. The fail-loud paths are covered by
// the existing TestScope_ZeroValueIsRejected (zero scope errors propagate
// through contentFiles) and code review of the suffix prefix check.

func TestEachContentFile_HonorsExcludeRels(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeFile(t, tmp, "keep.yaml", "k1: v1")
	writeFile(t, tmp, "drop.yaml", "k2: v2")

	scope := DirsScope(tmp, []string{"."}, ExcludeRels("drop.yaml"))
	var got []string
	EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc ContentContext) {
		got = append(got, fc.Rel)
	})
	if len(got) != 1 || got[0] != "keep.yaml" {
		t.Errorf("ExcludeRels not honored: got %v, want [keep.yaml]", got)
	}
}
