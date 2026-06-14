package prodscan

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	return root
}

func TestHasNestedModuleRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"cmd/gocell/go.mod":     "module x/cmd/gocell\n",
		"cmd/gocell/main.go":    "package main\n",
		"cmd/corebundle/go.mod": "module x/cmd/corebundle\n",
		"framework/kernel/k.go": "package kernel\n", // plain production, no nested go.mod
	})
	if !HasNestedModuleRoot(filepath.Join(root, "cmd")) {
		t.Errorf("HasNestedModuleRoot(cmd) = false, want true (cmd holds member modules)")
	}
	if HasNestedModuleRoot(filepath.Join(root, "framework/kernel")) {
		t.Errorf("HasNestedModuleRoot(framework/kernel) = true, want false (no nested go.mod)")
	}
	if HasNestedModuleRoot(filepath.Join(root, "does-not-exist")) {
		t.Errorf("HasNestedModuleRoot(missing) = true, want false")
	}
}

// TestPatternsSkipsMultiMemberSatelliteParents pins the #1565 production-scan
// boundary: a real workspace's multi-member satellite parents (cmd/adapters/
// examples) and module-root dirs (cellmodules) are pruned (deferred to gh #1590),
// while plain production layers (framework/kernel) are kept.
func TestPatternsSkipsMultiMemberSatelliteParents(t *testing.T) {
	root := writeTree(t, map[string]string{
		"framework/kernel/k.go":    "package kernel\n",
		"cmd/gocell/go.mod":        "module x/cmd/gocell\n",
		"cmd/gocell/main.go":       "package main\n",
		"adapters/postgres/go.mod": "module x/adapters/postgres\n",
		"adapters/postgres/pg.go":  "package postgres\n",
		"cellmodules/go.mod":       "module x/cellmodules\n",
		"cellmodules/c.go":         "package cellmodules\n",
	})
	got := map[string]bool{}
	for _, p := range Patterns(root) {
		got[p] = true
	}
	if !got["./framework/kernel/..."] {
		t.Errorf("Patterns must keep ./framework/kernel/...; got %v", Patterns(root))
	}
	for _, skipped := range []string{"./cmd/...", "./adapters/...", "./cellmodules/..."} {
		if got[skipped] {
			t.Errorf("Patterns must skip %q (satellite parent / module root deferred to #1590); got %v", skipped, Patterns(root))
		}
	}
}

// TestPatternsKeepsSingleModuleFixtureDirs proves the prune is real-workspace-only:
// a single-module fixture writes cmd/pkg/... as part of its ONE module (no nested
// go.mod), so those dirs stay scannable.
func TestPatternsKeepsSingleModuleFixtureDirs(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":      "module x\n",
		"cmd/main.go": "package main\n",
		"pkg/util.go": "package pkg\n",
	})
	got := map[string]bool{}
	for _, p := range Patterns(root) {
		got[p] = true
	}
	for _, kept := range []string{"./cmd/...", "./pkg/..."} {
		if !got[kept] {
			t.Errorf("single-module fixture: Patterns must keep %q; got %v", kept, Patterns(root))
		}
	}
}
