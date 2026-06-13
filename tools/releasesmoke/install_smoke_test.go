//go:build releasesmoke

// INVARIANT: INSTALLABLE-GO-INSTALL-SMOKE-01
//
// Proves an EXTERNAL consumer can `go install
// github.com/ghbvf/gocell/cmd/gocell@vX.Y.Z` against the RELEASE-STRIPPED CLI
// module — and, as the load-bearing red case, that the SAME module with its local
// `replace` directives still present is REJECTED by the toolchain. Hermetic: a
// local file GOPROXY publishes the bumped library set (reusing RELEASE-EXTERNAL-GET-01's
// proxy builder) plus an internal-deps-only projection of cmd/gocell as a stub
// `main` package — no real git tag, no network. This is the PR-time machine-checked
// half of #2045's "go install the CLI at a version" promise (the release.yml
// post-publish `go install` smoke is the on-real-tag half).
//
// # Why both a green and a red case
//
// `go install pkg@version` is rejected OUTRIGHT when the target module's go.mod
// carries ANY replace directive — that rejection is the entire reason cmd/gocell
// needs release-time replace-strip ([modrelease.StripReplaceAndPin]). The green
// case publishes cmd/gocell with the strip applied (no replace, internal requires
// pinned) and asserts `go install` SUCCEEDS and emits the binary. The red case
// publishes cmd/gocell with replace KEPT (only require-bumped via BumpModule, so
// the ONLY difference from green is the replace presence) and asserts `go install`
// FAILS naming the replace directive — proving the strip is load-bearing and the
// green path is not a vacuous pass.
//
// # Grading: Medium (execution guard, same ceiling as RELEASE-EXTERNAL-GET-01)
//
// "go install accepts the stripped shape / rejects the replace-bearing shape" is
// only checkable by running the real toolchain against the real strip transform
// output — a go.mod is data, not a type, so it is not Hard-expressible. It is far
// above Soft (it executes `go install`, asserting both the success AND the
// load-bearing rejection). The strip transform's BYTE output is frozen separately
// by INSTALLABLE-STRIP-TRANSFORM-01 (Hard); this smoke proves that frozen shape is
// actually installable end-to-end. Faithfulness gaps (network-inherent, identical
// to RELEASE-EXTERNAL-GET-01's residual): real VCS tag→version mapping, GOSUMDB
// interop, and the external (non-gocell) dependency closure — the "run once by hand
// on the real tag" remainder, exercised by the release.yml post-publish smoke.

// INVARIANT: INSTALLABLE-GO-INSTALL-SMOKE-01 (see file header for the full
// statement, grading, and the load-bearing red case).

package releasesmoke_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/gomodutil"
	"github.com/ghbvf/gocell/tools/modrelease"
)

// TestExternalInstall_StrippedCmdGocell_Resolves is the green path: publish the
// bumped library set + cmd/gocell with its go.mod release-STRIPPED (replace
// removed, internal requires pinned) to a hermetic file GOPROXY, then `go install
// github.com/ghbvf/gocell/cmd/gocell@synthVersion` and assert it resolves and
// emits the binary — proving the stripped shape is externally go-install-able.
func TestExternalInstall_StrippedCmdGocell_Resolves(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping external go install smoke in -short mode (spawns go install, ~10-40s)")
	}
	goBin := mustGo(t)
	root := repoRoot(t)
	proxy := buildProxy(t, root, synthVersion)
	importPath := publishInstallableBinary(t, proxy, root, synthVersion, true /* strip */)

	gobin := filepath.Join(t.TempDir(), "bin")
	env := append(offlineEnv(t, proxy), "GOBIN="+gobin)
	out, err := run(goBin, t.TempDir(), env, "install", importPath+"@"+synthVersion)
	if err != nil {
		t.Fatalf("INSTALLABLE-GO-INSTALL-SMOKE-01: `go install %s@%s` of the STRIPPED CLI failed "+
			"(it must resolve — no replace, internal requires pinned):\n%v\n%s", importPath, synthVersion, err, out)
	}
	// The binary's name is the module path's last segment ("gocell").
	bin := filepath.Join(gobin, filepath.Base(importPath))
	if _, statErr := os.Stat(bin); statErr != nil {
		t.Fatalf("INSTALLABLE-GO-INSTALL-SMOKE-01: go install reported success but produced no binary at %s: %v\n%s",
			bin, statErr, out)
	}
}

// TestExternalInstall_ReplaceKept_Rejected is the load-bearing red case: publish
// cmd/gocell with replace KEPT (require-bumped only, so the sole difference from
// the green path is the replace presence) and assert `go install` is REJECTED with
// an error naming the replace directive. This proves the strip in the green path is
// load-bearing, not decorative — without it `go install pkg@version` fails.
func TestExternalInstall_ReplaceKept_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping external go install smoke in -short mode")
	}
	goBin := mustGo(t)
	root := repoRoot(t)
	proxy := buildProxy(t, root, synthVersion)
	importPath := publishInstallableBinary(t, proxy, root, synthVersion, false /* keep replace */)

	gobin := filepath.Join(t.TempDir(), "bin")
	env := append(offlineEnv(t, proxy), "GOBIN="+gobin)
	out, err := run(goBin, t.TempDir(), env, "install", importPath+"@"+synthVersion)
	if err == nil {
		t.Fatalf("INSTALLABLE-GO-INSTALL-SMOKE-01: `go install %s@%s` SUCCEEDED with replace still present — "+
			"the strip is not load-bearing and the green path is vacuous:\n%s", importPath, synthVersion, out)
	}
	// The failure must be the replace-directive rejection, not something incidental.
	if !strings.Contains(out, "replace") {
		t.Fatalf("INSTALLABLE-GO-INSTALL-SMOKE-01: go install failed but not on the replace directive — "+
			"unexpected cause (the red case must isolate replace as the blocker):\n%s", out)
	}
}

// publishInstallableBinary publishes the single installable binary (cmd/gocell)
// into proxyRoot at version, as an internal-deps-only projection wrapped in a stub
// `main` package. When strip is true the go.mod is release-STRIPPED
// ([modrelease.StripReplaceAndPin]: replace removed + internal requires pinned);
// when false it is only require-bumped ([modrelease.BumpModule]) with replace KEPT
// — so the green and red callers differ ONLY in replace presence. Returns the
// installable module's import path.
func publishInstallableBinary(t *testing.T, proxyRoot, root, version string, strip bool) string {
	t.Helper()
	prefix, err := gomodutil.ReadModulePath(root)
	if err != nil {
		t.Fatalf("read root module path: %v", err)
	}
	mods, err := modrelease.InstallableBinaries(root)
	if err != nil {
		t.Fatalf("InstallableBinaries: %v", err)
	}
	if len(mods) == 0 {
		t.Fatal("anti-vacuity: no installable binaries enumerated (expected cmd/gocell)")
	}
	m := mods[0]
	goMod := stageInstallableGoMod(t, filepath.Join(root, m.Dir), prefix, version, strip)
	projected, internalImports := internalProjection(t, goMod, prefix)
	writeProxyModule(t, proxyRoot, m.ImportPath, version, projected, stubMainPackage(internalImports))
	return m.ImportPath
}

// stageInstallableGoMod copies modDir/go.mod into a temp dir and applies the
// release transform: StripReplaceAndPin (strip=true, the real installable-binary
// release shape) or BumpModule (strip=false, require-bumped but replace KEPT).
// Returns the resulting go.mod bytes.
func stageInstallableGoMod(t *testing.T, modDir, prefix, version string, strip bool) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Clean(filepath.Join(modDir, "go.mod")))
	if err != nil {
		t.Fatalf("read %s/go.mod: %v", modDir, err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Clean(filepath.Join(stage, "go.mod")), src, 0o644); err != nil {
		t.Fatalf("stage go.mod: %v", err)
	}
	if strip {
		if _, err := modrelease.StripReplaceAndPin(stage, prefix, version); err != nil {
			t.Fatalf("StripReplaceAndPin(%s): %v", modDir, err)
		}
	} else {
		if _, err := modrelease.BumpModule(stage, prefix, version); err != nil {
			t.Fatalf("BumpModule(%s): %v", modDir, err)
		}
	}
	out, err := os.ReadFile(filepath.Clean(filepath.Join(stage, "go.mod")))
	if err != nil {
		t.Fatalf("read staged go.mod: %v", err)
	}
	return out
}

// stubMainPackage renders a minimal compilable `main` package that blank-imports
// the module's internal siblings, so `go install` traverses the internal
// dependency graph (forcing version resolution of the pinned requires) and emits a
// real binary — without importing anything external.
func stubMainPackage(internalImports []string) string {
	var b strings.Builder
	b.WriteString("package main\n")
	if len(internalImports) > 0 {
		b.WriteString("\nimport (\n")
		for _, imp := range internalImports {
			b.WriteString("\t_ \"" + imp + "\"\n")
		}
		b.WriteString(")\n")
	}
	b.WriteString("\nfunc main() {}\n")
	return b.String()
}
