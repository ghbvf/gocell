package prodscan

import (
	"os"
	"path/filepath"
	"slices"
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
		"cmd/gocell/go.mod":               "module x/cmd/gocell\n",
		"cmd/gocell/main.go":              "package main\n",
		"cmd/corebundle/go.mod":           "module x/cmd/corebundle\n",
		"cellmodules/go.mod":              "module x/cellmodules\n",
		"cellmodules/grpclistener/go.mod": "module x/cellmodules/grpclistener\n",
		"framework/kernel/k.go":           "package kernel\n", // plain production, no nested go.mod
	})
	if !HasNestedModuleRoot(filepath.Join(root, "cmd")) {
		t.Errorf("HasNestedModuleRoot(cmd) = false, want true (cmd holds member modules)")
	}
	if HasNestedModuleRoot(filepath.Join(root, "cellmodules")) {
		t.Errorf("HasNestedModuleRoot(cellmodules) = true, want false (module roots are not expandable parents)")
	}
	if HasNestedModuleRoot(filepath.Join(root, "framework", "kernel")) {
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

// TestPatternsWithSatellites pins #2136: PatternsWithSatellites is the duration
// gates' opt-in to satellite coverage — it widens PatternsExtended with EXACTLY the
// multi-member satellite parent prefixes (cmd/adapters/examples) that Patterns
// prunes. Anti-vacuity: in a real multi-member workspace the increment must be
// NON-EMPTY (and those prefixes must be ABSENT from PatternsExtended, proving the
// widening is real, not already present); in a single-module fixture the increment
// must be EMPTY (== PatternsExtended).
func TestPatternsWithSatellites(t *testing.T) {
	t.Run("real workspace adds satellite parents", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"framework/kernel/k.go":    "package kernel\n",
			"cmd/gocell/go.mod":        "module x/cmd/gocell\n",
			"cmd/gocell/main.go":       "package main\n",
			"cmd/corebundle/go.mod":    "module x/cmd/corebundle\n",
			"cmd/corebundle/main.go":   "package main\n",
			"adapters/postgres/go.mod": "module x/adapters/postgres\n",
			"adapters/postgres/pg.go":  "package postgres\n",
			"examples/iot/go.mod":      "module x/examples/iot\n",
			"examples/iot/main.go":     "package main\n",
		})
		base := map[string]bool{}
		for _, p := range PatternsExtended(root) {
			base[p] = true
		}
		got := map[string]bool{}
		for _, p := range PatternsWithSatellites(root) {
			got[p] = true
		}
		for _, sat := range []string{"./cmd/...", "./adapters/...", "./examples/..."} {
			if base[sat] {
				t.Fatalf("PatternsExtended unexpectedly contains %q; the satellite increment is vacuous", sat)
			}
			if !got[sat] {
				t.Errorf("PatternsWithSatellites must add %q; got %v", sat, PatternsWithSatellites(root))
			}
		}
		for p := range base {
			if !got[p] {
				t.Errorf("PatternsWithSatellites dropped base pattern %q (must be a superset of PatternsExtended)", p)
			}
		}
		if len(PatternsWithSatellites(root)) <= len(PatternsExtended(root)) {
			t.Errorf("PatternsWithSatellites must strictly widen PatternsExtended in a multi-member workspace; "+
				"ext=%v full=%v", PatternsExtended(root), PatternsWithSatellites(root))
		}
	})

	t.Run("single-module fixture adds nothing", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"go.mod":      "module x\n",
			"cmd/main.go": "package main\n",
			"pkg/util.go": "package pkg\n",
		})
		ext := PatternsExtended(root)
		full := PatternsWithSatellites(root)
		// Content equality, not just length: a regression that dropped a base
		// pattern while adding a spurious one would keep the length equal but
		// must still fail here.
		if !slices.Equal(ext, full) {
			t.Errorf("single-module fixture: PatternsWithSatellites must equal PatternsExtended "+
				"(no nested go.mod → empty satellite increment); ext=%v full=%v", ext, full)
		}
	})

	t.Run("real workspace adds top-level single-module roots", func(t *testing.T) {
		// #2164: PatternsWithSatellites must compose the module-root increment too,
		// so the duration gates (PROD-DURATION-CONST-01 / TEST-TIME-LITERAL-01) cover
		// corecells/cellmodules — the symmetric counterpart of the satellite increment.
		root := writeTree(t, map[string]string{
			"framework/kernel/k.go": "package kernel\n",
			"corecells/go.mod":      "module x/corecells\n",
			"corecells/c.go":        "package corecells\n",
			"cellmodules/go.mod":    "module x/cellmodules\n",
			"cellmodules/m.go":      "package cellmodules\n",
		})
		ext := map[string]bool{}
		for _, p := range PatternsExtended(root) {
			ext[p] = true
		}
		got := map[string]bool{}
		for _, p := range PatternsWithSatellites(root) {
			got[p] = true
		}
		for _, mod := range []string{"./corecells/...", "./cellmodules/..."} {
			if ext[mod] {
				t.Fatalf("PatternsExtended unexpectedly contains %q; the module-root increment is vacuous", mod)
			}
			if !got[mod] {
				t.Errorf("PatternsWithSatellites must add %q; got %v", mod, PatternsWithSatellites(root))
			}
		}
	})
}

// TestSatelliteParentPatterns covers the single-sourced satellite increment (#2147)
// that both PatternsWithSatellites and the OBS-01 scan compose onto their base.
func TestSatelliteParentPatterns(t *testing.T) {
	t.Run("real workspace yields exactly the multi-member parents", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"framework/kernel/k.go":    "package kernel\n",
			"cmd/gocell/go.mod":        "module x/cmd/gocell\n",
			"cmd/gocell/main.go":       "package main\n",
			"adapters/postgres/go.mod": "module x/adapters/postgres\n",
			"adapters/postgres/pg.go":  "package postgres\n",
			"examples/iot/go.mod":      "module x/examples/iot\n",
			"examples/iot/main.go":     "package main\n",
			// cellmodules is a plain module root (own go.mod, no nested member) — it
			// is NOT a satellite parent and must be excluded by !IsModuleRoot.
			"cellmodules/go.mod": "module x/cellmodules\n",
			"cellmodules/m.go":   "package cellmodules\n",
			// tests/ + tools/ are PatternsExtended-only scope, never satellites.
			"tests/e2e/e.go": "package e2e\n",
			"tools/t/t.go":   "package t\n",
		})
		got := SatelliteParentPatterns(root)
		gotSet := map[string]bool{}
		for _, p := range got {
			gotSet[p] = true
		}
		for _, want := range []string{"./cmd/...", "./adapters/...", "./examples/..."} {
			if !gotSet[want] {
				t.Errorf("SatelliteParentPatterns missing %q; got %v", want, got)
			}
		}
		if len(got) != 3 {
			t.Errorf("SatelliteParentPatterns = %v, want exactly the 3 multi-member parents "+
				"(no framework/cellmodules/tests/tools)", got)
		}
		// OBS-01 composition = Patterns + increment, and must NOT pull in tests/ or
		// tools/ (PatternsExtended scope) — the split is by base, not loader capability.
		obs01 := map[string]bool{}
		for _, p := range append(Patterns(root), SatelliteParentPatterns(root)...) {
			obs01[p] = true
		}
		for _, forbidden := range []string{"./tests/...", "./tools/..."} {
			if obs01[forbidden] {
				t.Errorf("OBS-01 composition (Patterns + satellites) must exclude %q", forbidden)
			}
		}
	})

	t.Run("single-module fixture yields nothing", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"go.mod":      "module x\n",
			"cmd/main.go": "package main\n",
		})
		if got := SatelliteParentPatterns(root); len(got) != 0 {
			t.Errorf("SatelliteParentPatterns on single-module fixture = %v, want empty", got)
		}
	})
}

// TestModuleRootMemberPatterns covers the single-sourced module-root increment
// (#2164) — the sibling of SatelliteParentPatterns. SatelliteParentPatterns re-emits
// the MULTI-MEMBER parents (cmd/adapters/examples, !IsModuleRoot && HasNestedModuleRoot);
// ModuleRootMemberPatterns re-emits the TOP-LEVEL MODULE ROOTS (corecells/cellmodules,
// IsModuleRoot) that Patterns prunes via IsModuleRoot.
// Both OBS-01 and the duration gates compose this increment onto their base.
func TestModuleRootMemberPatterns(t *testing.T) {
	t.Run("real workspace yields exactly the top-level module roots", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"framework/kernel/k.go": "package kernel\n", // plain production layer, not a module root
			// multi-member parent — owned by SatelliteParentPatterns, NOT this increment.
			"cmd/gocell/go.mod":  "module x/cmd/gocell\n",
			"cmd/gocell/main.go": "package main\n",
			// Top-level module roots — own go.mod → THIS increment. A nested helper
			// module stays a separate workspace member without making the parent an
			// expandable satellite parent.
			"cellmodules/go.mod":              "module x/cellmodules\n",
			"cellmodules/m.go":                "package cellmodules\n",
			"cellmodules/grpclistener/go.mod": "module x/cellmodules/grpclistener\n",
			"cellmodules/grpclistener/g.go":   "package grpclistener\n",
			"corecells/go.mod":                "module x/corecells\n",
			"corecells/c.go":                  "package corecells\n",
		})
		got := ModuleRootMemberPatterns(root)
		gotSet := map[string]bool{}
		for _, p := range got {
			gotSet[p] = true
		}
		for _, want := range []string{"./cellmodules/...", "./corecells/..."} {
			if !gotSet[want] {
				t.Errorf("ModuleRootMemberPatterns missing %q; got %v", want, got)
			}
		}
		if len(got) != 2 {
			t.Errorf("ModuleRootMemberPatterns = %v, want exactly the 2 top-level module roots "+
				"(no framework layers, no multi-member parents)", got)
		}
		// The increment is genuinely NEW: its members appear in neither the base scan
		// nor the multi-member-parent increment (else composing it would be vacuous).
		base := map[string]bool{}
		for _, p := range append(Patterns(root), SatelliteParentPatterns(root)...) {
			base[p] = true
		}
		for _, p := range got {
			if base[p] {
				t.Errorf("ModuleRootMemberPatterns member %q already present in Patterns+SatelliteParentPatterns "+
					"— the increment is not additive", p)
			}
		}
	})

	t.Run("single-module fixture yields nothing", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"go.mod":            "module x\n",
			"corecells/repo.go": "package corecells\n", // part of the ONE module, no own go.mod
		})
		if got := ModuleRootMemberPatterns(root); len(got) != 0 {
			t.Errorf("ModuleRootMemberPatterns on single-module fixture = %v, want empty", got)
		}
	})
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
