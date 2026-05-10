package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

// TestLoadContentFiles_Errors exercises every fail-loud branch in the pure
// LoadContentFiles function. EachContentFile (the t.Fatalf-on-error wrapper)
// cannot be unit-tested for the same failures because t.Fatalf calls
// runtime.Goexit which cannot be intercepted via recover, and t.Run does not
// isolate Goexit between parent and child tests. The pure function makes
// every error path testable via plain returns-error assertions.
func TestLoadContentFiles_Errors(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	writeFile(t, tmp, "a.yaml", "k: v")
	validScope := DirsScope(tmp, []string{"."})

	cases := []struct {
		name        string
		scope       Scope
		suffixes    []string
		wantErr     bool
		wantSubstr  string // non-empty means err.Error() must contain this
		wantResults int    // expected len() when wantErr=false
	}{
		{
			name:       "nil_suffixes",
			scope:      validScope,
			suffixes:   nil,
			wantErr:    true,
			wantSubstr: "suffixes must be non-empty",
		},
		{
			name:       "suffix_without_leading_dot",
			scope:      validScope,
			suffixes:   []string{"yaml"},
			wantErr:    true,
			wantSubstr: `suffix "yaml" must start with '.'`,
		},
		{
			name:       "zero_value_scope",
			scope:      Scope{},
			suffixes:   []string{".yaml"},
			wantErr:    true,
			wantSubstr: "Scope zero value is invalid",
		},
		{
			name:       "module_scope_plus_include_testdata_propagates_setup_err",
			scope:      ModuleScope(tmp, IncludeTestdata()),
			suffixes:   []string{".yaml"},
			wantErr:    true,
			wantSubstr: "IncludeTestdata requires",
		},
		{
			name:        "non_existent_root_returns_empty_no_error",
			scope:       DirsScope(tmp, []string{"does/not/exist"}),
			suffixes:    []string{".yaml"},
			wantErr:     false,
			wantResults: 0, // matches Files() silently-skip semantics for missing roots
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := LoadContentFiles(tc.scope, tc.suffixes)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (results=%v)", got)
				}
				if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tc.wantResults {
				t.Errorf("got %d results, want %d", len(got), tc.wantResults)
			}
		})
	}
}

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
