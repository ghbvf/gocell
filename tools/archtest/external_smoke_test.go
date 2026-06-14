//go:build archtest

package archtest_test

// INVARIANT: ARCHTEST-EXTERNAL-SURFACE-01 (cross-module smoke)
//
// external_smoke_test.go proves the importable surface end-to-end from a
// SEPARATE module — the one thing the in-package TestRunStandardCellRules
// (same module) cannot: that an external Cell repo can `go get` tools/archtest,
// import it from its own _test.go, and have RunStandardCellRules scan ITS module
// (not GoCell's). This locks three things the in-repo dogfood leaves untested:
//
//   - the package compiles as a DEPENDENCY (no package-scope -update flag
//     redefinition panic — golden_harness_test.go's flag must stay test-only);
//   - internal/ loader primitives are not part of the imported surface (Go's
//     internal/ visibility, enforced at compile time of the consumer);
//   - findModuleRoot resolves the CONSUMER's go.mod (cwd-based), so the scan
//     target is the consumer module, while platform symbol paths stay anchored
//     to PlatformModulePath.
//
// It runs in a throwaway module under t.TempDir() (no checked-in nested go.mod /
// go.sum) with a `replace` onto this repo, and is skipped in -short (it spawns a
// nested `go test` that type-loads the consumer module — tens of seconds cold).
// Mirrors the lintgate_smoke_test.go exec idiom (Short + LookPath + TempDir).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalModuleSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cross-module smoke in -short mode (spawns a nested `go test`, ~20-60s cold)")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not in PATH; cross-module smoke skipped")
	}
	root := repoRootFromTest(t)

	tmp := t.TempDir()
	// A non-test production file with a BARE panic — the canonical
	// PANIC-REGISTERED-01 violation the standard set must catch in the consumer.
	writeSmokeFile(t, tmp, "boom.go",
		"package extsmoke\n\n// Boom contains a bare panic the rule must flag.\nfunc Boom() { panic(\"boom\") }\n")
	// The consumer's own arch test: imports the real archtest and runs the
	// curated set against its OWN module.
	writeSmokeFile(t, tmp, "arch_test.go",
		"package extsmoke\n\nimport (\n\t\"testing\"\n\n\t\"github.com/ghbvf/gocell/tools/archtest\"\n)\n\n"+
			"func TestConsumerArchitecture(t *testing.T) {\n\tarchtest.RunStandardCellRules(t, archtest.ConfigForExternalCell{})\n}\n")
	writeSmokeFile(t, tmp, "go.mod",
		"module gocell.example/extsmoke\n\ngo 1.25\n\n"+
			"require github.com/ghbvf/gocell/tools v0.0.0\n"+
			"require github.com/ghbvf/gocell/framework v0.0.0 // indirect\n\n"+
			"replace github.com/ghbvf/gocell/tools => "+filepath.Join(root, "tools")+"\n"+
			"replace github.com/ghbvf/gocell/framework => "+filepath.Join(root, "framework")+"\n")

	cmd := exec.Command(goBin, "test", "./...") //nolint:gosec // G204: const args; cwd is t.TempDir()
	cmd.Dir = tmp
	// -mod=mod lets go synthesize go.sum from the warm module cache (GOPROXY=off
	// keeps it offline); the replaced module's transitive deps are already cached
	// by this repo's own build.
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	out, err := cmd.CombinedOutput()

	// The consumer's TestConsumerArchitecture MUST fail (Boom's bare panic is a
	// violation), and the failure MUST name the rule — proving the imported rule
	// actually scanned the consumer module.
	if err == nil {
		t.Fatalf("expected the consumer `go test` to FAIL on the bare panic, but it passed.\n%s", out)
	}
	if !strings.Contains(string(out), "PANIC-REGISTERED-01") {
		t.Fatalf("consumer `go test` failed but not via PANIC-REGISTERED-01 — the imported rule "+
			"did not scan the consumer module as expected.\n%s", out)
	}
}

// repoRootFromTest returns this repository's workspace root (absolute), for the
// smoke module's replace directives. It walks up from the test's working
// directory to go.work so tests running from the tools module still resolve the
// repository root.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRootFromTest: no go.work found walking up from test cwd")
		}
		dir = parent
	}
}

func writeSmokeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
