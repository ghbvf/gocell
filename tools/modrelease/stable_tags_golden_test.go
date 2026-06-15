package modrelease

// INVARIANT: STABLE-RELEASE-TAG-SET-01
//
// The stable release push set — the bare vX.Y.Z release MARKER tag FIRST,
// followed by every publishable library module tag — is DERIVED by [StableTags]
// (marker via [tagPathFor] + [TagPaths]) and byte-locked into
// testdata/stable_tags.golden. This is a codegen-funnel+golden guard
// (ai-robust §载体 #1, Hard): adding/removing/renaming a go.work member, changing
// IsPublishable, or changing the marker shape shifts the derived set, the golden
// goes stale, and CI fails on the byte diff — the drift cannot be silent.
// Regenerate intentionally with `make update-modrelease-golden`.
//
// # Grading: Hard (golden byte-freeze of a derived set)
//
// The SET is derived from go.work (the single source the toolchain compiles) +
// the [tagPathFor] root-ref convention, and the OUTPUT bytes are frozen. This
// supersedes the pre-#2141 Medium content-scan that asserted the workflow
// hand-minted the marker in shell (RELEASE-STABLE-MARKER-TAG-01): the marker now
// lives in modrelease's StableTags surface, golden-locked here, and the workflow
// merely CONSUMES `--print-stable-tags` (consumed-shape still Medium-guarded by
// the repointed RELEASE-STABLE-MARKER-TAG-01). See #2141.
//
// # Anti-vacuity + synthetic red case (ai-robust §archtest 文件命名)
//
//   - Anti-vacuity: TestStableReleaseTagSet01 asserts len >= 3 (marker + >= 2
//     library tags), that the FIRST element is the bare vX.Y.Z marker, and that
//     the core framework tag is present — so a mis-resolved / empty / marker-less
//     set cannot pass green.
//   - Synthetic proof: TestStableReleaseTagSet01_MarkerFirst asserts StableTags ==
//     [bare marker] ++ TagPaths, i.e. the marker is prepended and the library tail
//     equals the library-only surface — proving StableTags is not a vacuous alias
//     of either surface.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stableReleaseTagSetRule = "STABLE-RELEASE-TAG-SET-01"

// renderStableTags serializes the stable push set in the same style as
// renderPublishable/renderInstallable so golden generation is consistent. The
// set is version-bearing, so the golden bakes testVersion (v1.2.3).
func renderStableTags(tags []string) string {
	var b strings.Builder
	b.WriteString("# Stable release tag set — DERIVED: bare vX.Y.Z marker FIRST + publishable library tags.\n")
	b.WriteString("# Rendered at version " + testVersion + ". Regenerate intentionally: make update-modrelease-golden\n")
	for _, t := range tags {
		fmt.Fprintf(&b, "%s\n", t)
	}
	return b.String()
}

// TestStableReleaseTagSet01 is the golden byte-freeze guard for the stable
// release push set (STABLE-RELEASE-TAG-SET-01, Hard).
func TestStableReleaseTagSet01(t *testing.T) {
	root := workspaceRootForTest(t)

	tags, err := StableTags(root, testVersion)
	if err != nil {
		t.Fatalf("%s: StableTags: %v", stableReleaseTagSetRule, err)
	}

	// Anti-vacuity: marker + the real publishable set is >= 3; the marker must be
	// the FIRST element (bare vX.Y.Z), and the core framework tag must be present.
	if len(tags) < 3 {
		t.Fatalf("%s: anti-vacuity failed: derived %d stable tags, want >= 3 (marker + >= 2 library)", stableReleaseTagSetRule, len(tags))
	}
	if tags[0] != testVersion {
		t.Fatalf("%s: anti-vacuity failed: first tag %q is not the bare vX.Y.Z marker %q", stableReleaseTagSetRule, tags[0], testVersion)
	}
	if !containsString(tags, "framework/"+testVersion) {
		t.Fatalf("%s: anti-vacuity failed: core framework tag framework/%s absent from %v", stableReleaseTagSetRule, testVersion, tags)
	}

	got := renderStableTags(tags)
	goldenPath := filepath.Clean(filepath.Join("testdata", "stable_tags.golden"))
	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("%s: write golden: %v", stableReleaseTagSetRule, err)
		}
		t.Logf("%s: wrote %s (%d tags)", stableReleaseTagSetRule, goldenPath, len(tags))
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%s: read golden (run `make update-modrelease-golden`): %v", stableReleaseTagSetRule, err)
	}
	if got != string(want) {
		t.Errorf("%s: stable release tag set drifted from testdata/stable_tags.golden.\n"+
			"A go.work member was added/removed/renamed, IsPublishable changed, or the marker shape changed. "+
			"If intentional, run `make update-modrelease-golden`.\n--- got ---\n%s\n--- want ---\n%s",
			stableReleaseTagSetRule, got, string(want))
	}
}

// TestStableReleaseTagSet01_MarkerFirst proves StableTags == [bare marker] ++
// TagPaths: the marker is prepended and the tail equals the library-only surface.
// This keeps StableTags from being a vacuous alias of either surface.
func TestStableReleaseTagSet01_MarkerFirst(t *testing.T) {
	root := workspaceRootForTest(t)

	libTags, err := TagPaths(root, testVersion)
	if err != nil {
		t.Fatalf("%s: TagPaths: %v", stableReleaseTagSetRule, err)
	}
	stable, err := StableTags(root, testVersion)
	if err != nil {
		t.Fatalf("%s: StableTags: %v", stableReleaseTagSetRule, err)
	}

	// The library surface must NOT contain the bare marker (post-#1565 no root
	// module), so StableTags genuinely adds it.
	if containsString(libTags, testVersion) {
		t.Fatalf("%s: TagPaths unexpectedly emits the bare marker %q — StableTags would be a no-op prepend", stableReleaseTagSetRule, testVersion)
	}
	if len(stable) != len(libTags)+1 {
		t.Fatalf("%s: StableTags len %d != TagPaths len %d + 1 (marker)", stableReleaseTagSetRule, len(stable), len(libTags))
	}
	if stable[0] != testVersion {
		t.Errorf("%s: StableTags[0] = %q, want bare marker %q first", stableReleaseTagSetRule, stable[0], testVersion)
	}
	for i, lt := range libTags {
		if stable[i+1] != lt {
			t.Errorf("%s: StableTags[%d] = %q, want library tag %q (tail must equal TagPaths)", stableReleaseTagSetRule, i+1, stable[i+1], lt)
		}
	}
}

// TestStableTagsRejectsBadVersion confirms StableTags rejects malformed versions
// (it routes through TagPaths → validReleaseVersion).
func TestStableTagsRejectsBadVersion(t *testing.T) {
	root := workspaceRootForTest(t)
	for _, bad := range []string{"1.2.3", "v1.2", "v1.2.3-rc1+meta", "", "v2.0.0"} {
		if _, err := StableTags(root, bad); err == nil {
			t.Errorf("%s: StableTags must reject version %q", stableReleaseTagSetRule, bad)
		}
	}
}
