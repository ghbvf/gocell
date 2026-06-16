package modrelease

// INVARIANT: INSTALLABLE-BINARY-SET-01
// INVARIANT: INSTALLABLE-STRIP-TRANSFORM-01
// INVARIANT: INSTALLABLE-REQUIRES-PUBLISHABLE-01
//
// These three invariants guard the release-time strip+pin pipeline for
// go-install-able binaries (cmd/gocell, #2045).
//
// # INSTALLABLE-BINARY-SET-01 (Hard — golden byte-freeze of a derived set)
//
// InstallableBinaries(root) is DERIVED from go.work filtered against the
// closed installableBinaries set, byte-frozen into testdata/installable.golden.
// Regenerate intentionally with `make update-modrelease-golden`.
//
// # INSTALLABLE-STRIP-TRANSFORM-01 (Hard — fixture golden byte-freeze)
//
// stripAndPinBytes on a fixture go.mod input must yield byte-identical output
// to testdata/installable_strip_expected.go.mod. Proves: every replace stripped,
// internal requires pinned, external/indirect preserved.
//
// # INSTALLABLE-REQUIRES-PUBLISHABLE-01 (Medium — dynamic scan of the real go.mod)
//
// Reads the real cmd/gocell/go.mod and asserts that after stripAndPinBytes:
//   (a) output has 0 replace lines
//   (b) every pinned internal require path is in PublishableModules(root)
//
// Grading: Medium (not Hard) because this is a dynamic runtime scan of the
// real go.mod on disk, not a byte-frozen golden or type-system constraint —
// the go.mod is data, so violations are detectable only by executing the
// check at test time (archtest typed scan / runtime guard category).
//
// This prevents "add an un-publishable internal dep to cmd/gocell" or
// "re-introduce a replace" from being silently merged.
//
// Anti-vacuity: the scan loop asserts it checked at least one internal require
// (see TestInstallableRequiresPublishable01). If cmd/gocell were to lose all
// internal deps the counter triggers, surfacing a possible transform bug.
//
// Upstream: [StripReplaceAndPin] is the single sanctioned caller of
// stripAndPinBytes for release (invoked by release.yml --installable step).
// This is a calling convention, not a machine-enforced sealed funnel —
// Hard-ening (archtest restricting callers) is a future option but not yet
// implemented. The export of [StripReplaceAndPin] means a second caller could
// exist without triggering CI.
//
// Downstream strength: release.yml go install smoke (post-PR gate) verifies
// the produced tree is go-install-able at the tagged version.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/workspace"
)

const installableBinarySetRule = "INSTALLABLE-BINARY-SET-01"

// renderInstallable serializes the installable module set in the same style as
// renderPublishable so golden generation is consistent.
func renderInstallable(mods []workspace.Module) string {
	var b strings.Builder
	b.WriteString("# Installable binary set — DERIVED from go.work filtered by installableBinaries.\n")
	b.WriteString("# Regenerate intentionally: make update-modrelease-golden\n")
	b.WriteString("# <reldir>\\t<import-path>\n")
	for _, m := range mods {
		fmt.Fprintf(&b, "%s\t%s\n", filepath.ToSlash(filepath.Clean(m.Dir)), m.ImportPath)
	}
	return b.String()
}

// TestInstallableBinarySet01 is the golden byte-freeze guard for the
// installable binary set (INSTALLABLE-BINARY-SET-01, Hard).
func TestInstallableBinarySet01(t *testing.T) {
	root := workspaceRootForTest(t)

	mods, err := InstallableBinaries(root)
	if err != nil {
		t.Fatalf("%s: InstallableBinaries: %v", installableBinarySetRule, err)
	}

	// Anti-vacuity: set must be non-empty and contain cmd/gocell.
	if len(mods) == 0 {
		t.Fatalf("%s: anti-vacuity failed: derived 0 installable binaries, want >= 1", installableBinarySetRule)
	}
	var hasGocell bool
	for _, m := range mods {
		if filepath.ToSlash(filepath.Clean(m.Dir)) == "cmd/gocell" {
			hasGocell = true
		}
	}
	if !hasGocell {
		t.Fatalf("%s: anti-vacuity failed: cmd/gocell absent from installable set %v", installableBinarySetRule, mods)
	}

	got := renderInstallable(mods)
	goldenPath := filepath.Clean(filepath.Join("testdata", "installable.golden"))
	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("%s: write golden: %v", installableBinarySetRule, err)
		}
		t.Logf("%s: wrote %s (%d binaries)", installableBinarySetRule, goldenPath, len(mods))
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%s: read golden (run `make update-modrelease-golden`): %v", installableBinarySetRule, err)
	}
	if got != string(want) {
		t.Errorf("%s: installable binary set drifted from testdata/installable.golden.\n"+
			"A go.work member was added/removed/renamed, or installableBinaries changed. If intentional, run "+
			"`make update-modrelease-golden`.\n--- got ---\n%s\n--- want ---\n%s",
			installableBinarySetRule, got, string(want))
	}
}

// TestInstallableBinarySet01_FilterProof is the synthetic red case: a fixture
// go.work containing cmd/gocell + cmd/corebundle + examples/foo; only cmd/gocell
// must survive (proving the filter is not vacuous).
func TestInstallableBinarySet01_FilterProof(t *testing.T) {
	root := workspaceRootForTest(t)
	fixture := filepath.Join(root, "tools", "modrelease", "testdata", "installable_set_fixture")

	mods, err := InstallableBinaries(fixture)
	if err != nil {
		t.Fatalf("%s: InstallableBinaries(fixture): %v", installableBinarySetRule, err)
	}

	got := make(map[string]bool, len(mods))
	for _, m := range mods {
		got[filepath.ToSlash(filepath.Clean(m.Dir))] = true
	}

	// Only cmd/gocell should survive.
	if !got["cmd/gocell"] {
		t.Errorf("%s: filter wrongly excluded cmd/gocell from fixture", installableBinarySetRule)
	}
	// cmd/corebundle and examples/foo must be excluded.
	wantDropped := []string{"cmd/corebundle", "examples/foo"}
	for _, d := range wantDropped {
		if got[d] {
			t.Errorf("%s: filter failed to drop %q (filter is vacuous — does not restrict to installableBinaries)", installableBinarySetRule, d)
		}
	}
	if len(mods) != 1 {
		t.Errorf("%s: fixture yielded %d installable modules %v, want exactly [cmd/gocell]",
			installableBinarySetRule, len(mods), keys(got))
	}
}

// TestInstallableStripTransform01 is the byte-level golden guard for the
// strip+pin transform (INSTALLABLE-STRIP-TRANSFORM-01, Hard).
func TestInstallableStripTransform01(t *testing.T) {
	const rule = "INSTALLABLE-STRIP-TRANSFORM-01"

	input, err := os.ReadFile(filepath.Clean(filepath.Join("testdata", "installable_strip_input.go.mod")))
	if err != nil {
		t.Fatalf("%s: read input fixture: %v", rule, err)
	}
	expected, err := os.ReadFile(filepath.Clean(filepath.Join("testdata", "installable_strip_expected.go.mod")))
	if err != nil {
		t.Fatalf("%s: read expected fixture: %v", rule, err)
	}

	got, err := stripAndPinBytes(input, "github.com/ghbvf/gocell", "v1.2.3")
	if err != nil {
		t.Fatalf("%s: stripAndPinBytes: %v", rule, err)
	}

	// Primary assertion: byte equality with expected.
	if !bytes.Equal(got, expected) {
		t.Errorf("%s: strip+pin output differs from testdata/installable_strip_expected.go.mod\n--- got ---\n%s\n--- want ---\n%s",
			rule, got, expected)
	}

	// Explicit: output must contain 0 replace lines.
	if strings.Contains(string(got), "replace") {
		t.Errorf("%s: output contains 'replace' — all replace directives must be stripped:\n%s", rule, got)
	}

	// External requires (e.g. golang.org/x/tools) must be preserved.
	if !strings.Contains(string(got), "golang.org/x/tools") {
		t.Errorf("%s: external require golang.org/x/tools not found in output — must be preserved:\n%s", rule, got)
	}

	// indirect marker must be preserved for indirect deps.
	if !strings.Contains(string(got), "// indirect") {
		t.Errorf("%s: indirect marker not found in output — must be preserved:\n%s", rule, got)
	}

	// Idempotency: running on expected output again must give the same result.
	got2, err := stripAndPinBytes(expected, "github.com/ghbvf/gocell", "v1.2.3")
	if err != nil {
		t.Fatalf("%s: idempotency re-run: %v", rule, err)
	}
	if !bytes.Equal(got2, expected) {
		t.Errorf("%s: not idempotent: second pass differs from expected:\n--- got2 ---\n%s\n--- expected ---\n%s",
			rule, got2, expected)
	}
}

// TestInstallableRequiresPublishable01 reads the REAL cmd/gocell/go.mod,
// applies stripAndPinBytes, and asserts:
//
//	(a) output has 0 replace lines
//	(b) every pinned internal require path is in PublishableModules(root)
//
// (INSTALLABLE-REQUIRES-PUBLISHABLE-01, Medium — see §Grading above; this is a
// dynamic runtime scan, not a Hard byte/type freeze).
func TestInstallableRequiresPublishable01(t *testing.T) {
	const rule = "INSTALLABLE-REQUIRES-PUBLISHABLE-01"

	root := workspaceRootForTest(t)

	rootPrefix, err := workspace.CorePrefix(root)
	if err != nil {
		t.Fatalf("%s: read root module path: %v", rule, err)
	}

	realGoMod := filepath.Join(root, "cmd", "gocell", "go.mod")
	realData, err := os.ReadFile(filepath.Clean(realGoMod))
	if err != nil {
		t.Fatalf("%s: read cmd/gocell/go.mod: %v", rule, err)
	}

	out, err := stripAndPinBytes(realData, rootPrefix, "v1.2.3")
	if err != nil {
		t.Fatalf("%s: stripAndPinBytes: %v", rule, err)
	}

	// (a) No replace lines.
	if strings.Contains(string(out), "replace") {
		t.Errorf("%s: (a) output still contains 'replace' lines:\n%s", rule, out)
	}

	// (b) Every pinned internal require must be in PublishableModules.
	publishable, err := PublishableModules(root)
	if err != nil {
		t.Fatalf("%s: PublishableModules: %v", rule, err)
	}
	pubPaths := make(map[string]bool, len(publishable))
	for _, m := range publishable {
		pubPaths[m.ImportPath] = true
	}

	// Parse the output to find internal requires that were pinned to v1.2.3.
	var checked int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, rootPrefix) {
			continue
		}
		// Line is like "github.com/ghbvf/gocell/tools v1.2.3 // indirect"
		// or "github.com/ghbvf/gocell v1.2.3"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		modPath := fields[0]
		modVer := fields[1]
		if modVer != "v1.2.3" {
			continue // not a pinned internal require
		}
		checked++
		if !pubPaths[modPath] {
			t.Errorf("%s: (b) internal require %q pinned to v1.2.3 but not in PublishableModules — "+
				"cmd/gocell depends on an un-publishable internal module", rule, modPath)
		}
	}
	// Anti-vacuity: if stripAndPinBytes returned 0 internal requires (e.g. all
	// internal deps were removed from cmd/gocell, or the transform has a bug
	// returning empty output), the loop above trivially passes without checking
	// anything. Fail loudly so the gap is detected rather than silently skipped.
	if checked == 0 {
		t.Errorf("%s: anti-vacuity failed: 0 internal requires checked — vacuous "+
			"(cmd/gocell has no internal requires after strip, or stripAndPinBytes returned empty output)", rule)
	}
}

// TestInstallableTagPaths covers version validation and tag shape.
func TestInstallableTagPaths(t *testing.T) {
	root := workspaceRootForTest(t)

	// Invalid versions must be rejected.
	for _, bad := range []string{"1.2.3", "v1.2", "v1.2.3-rc1+meta", "", "v2.0.0", "v3.1.4"} {
		if _, err := InstallableTagPaths(root, bad); err == nil {
			t.Errorf("InstallableTagPaths must reject version %q", bad)
		}
	}

	tags, err := InstallableTagPaths(root, testVersion)
	if err != nil {
		t.Fatalf("InstallableTagPaths: %v", err)
	}

	// Must contain cmd/gocell/v1.2.3.
	if !containsString(tags, "cmd/gocell/"+testVersion) {
		t.Errorf("cmd/gocell/%s missing from InstallableTagPaths result %v", testVersion, tags)
	}
}
