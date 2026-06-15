//go:build archtest

// INVARIANT: RELEASE-STABLE-MARKER-TAG-01
package archtest

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// tagModulesStepName is the release.yml step that creates + pushes the stable
// release's git tags. The marker invariant binds to THIS step's run block.
const tagModulesStepName = "Tag modules"

// consumeStableTagsRe matches the stable push-set construction that CONSUMES
// modrelease's single-source stable tag set into the push array:
//
//	mapfile -t tags < <(... --print-stable-tags)
//
// Post-#2141 modrelease OWNS the bare vX.Y.Z marker (StableTags, byte-locked by
// STABLE-RELEASE-TAG-SET-01); the workflow consumes that golden set verbatim
// rather than hand-minting the marker. Matching the flag literal is sufficient —
// it is the one token that proves the single source is consumed. Comment-stripped
// command lines are matched (runCommandLines), so a commented example cannot
// satisfy the guard.
var consumeStableTagsRe = regexp.MustCompile(`--print-stable-tags`)

// shellMintMarkerRe matches the PRE-#2141 hand-mint shape that prepended the bare
// $TAG marker to modrelease's library tags in shell:
//
//	tags=("$TAG" "${module_tags[@]}")
//
// Post-#2141 the marker lives in modrelease's StableTags (Hard golden), NOT shell
// text — so this shape is BANNED: its reappearance means marker authority drifted
// back out of modrelease into un-golden-locked workflow shell. Whitespace-tolerant
// inside the array literal; comment-stripped lines bind it to real command text.
var shellMintMarkerRe = regexp.MustCompile(`tags=\(\s*"\$TAG"`)

// staleAssertionMarker is the pre-#1565 broken guard's error string: it required
// the bare $TAG to appear in modrelease's derived (library-only) tag set, which
// always exits 1 post-#1565 (--print-tag-paths correctly never emits a bare tag).
// Its reappearance is a regression — banned alongside the shell hand-mint.
const staleAssertionMarker = "root tag $TAG missing from derived tag set"

type markerWorkflow struct {
	Jobs map[string]markerJob `yaml:"jobs"`
}

type markerJob struct {
	Steps []markerStep `yaml:"steps"`
}

type markerStep struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

// TestReleaseStableMarkerTag enforces RELEASE-STABLE-MARKER-TAG-01 across every
// .github/workflows/*.y{a,}ml: the stable "Tag modules" step must CONSUME
// modrelease's single-source `--print-stable-tags` (the bare $TAG marker FIRST +
// library tags), must NOT hand-mint the marker in shell (`tags=("$TAG" …)`), and
// must NOT carry the pre-#1565 "$TAG must be in the derived set" assertion.
//
// Why it matters: #1565 moved the core into framework/, so the repo root holds no
// module and modrelease's TagPaths emits only <subdir>/vX.Y.Z library tags — never
// a bare vX.Y.Z. But the bare vX.Y.Z is the repo-level RELEASE MARKER that
// `gh release create --verify-tag`, goreleaser {{.Tag}}, and the verify-job resume
// sentinel (`refs/tags/$tag`) all resolve against (k8s/gopls convention: bare =
// release marker, <subdir>/vX = the go-get module tag). #2134 first restored the
// marker by HAND-MINTING it in workflow shell (`tags=("$TAG" …)`), guarded by this
// test's prior Medium content-scan. #2141 Hard-ized that: modrelease now OWNS the
// marker via [modrelease.StableTags], byte-locked by STABLE-RELEASE-TAG-SET-01
// (testdata/stable_tags.golden). The workflow consumes that golden set verbatim;
// this guard now keeps the CONSUMED shape from regressing (back to hand-mint, to a
// marker-less library-only consume, or to the stale assertion).
//
// AI-robust grade: this guard is Medium content-scan of the workflow's CONSUMED
// shape (a YAML/shell seam the type system can't express). The Hard guarantee —
// that the marker + library set is exactly correct — moved to the golden
// (STABLE-RELEASE-TAG-SET-01, Hard): together they form a closed funnel, golden
// upstream (the SET) + this scan downstream (the workflow actually consumes it,
// not a re-derived/hand-minted substitute). Dropping `--print-stable-tags`, adding
// a `tags=("$TAG" …)` hand-mint, or re-adding the stale assertion is a
// reviewer-visible diff that fails this test.
//
// Blind spot (disclosed):
//   - This scans the run-block TEXT, not the executed git pushes — it proves the
//     step consumes the single source, not that the push succeeds. Push success /
//     tag-set atomicity is covered at release time by the --atomic push +
//     `gh release create --verify-tag` + the "Validate satellite tag set (stable
//     resume)" step.
//   - consumeStableTagsRe binds to the `--print-stable-tags` flag literal. A
//     maintainer who pipes the flag through an intermediate var / wrapper script
//     could consume the single source without the literal appearing in this step's
//     run block (evading the positive match); the golden still locks the SET.
//   - shellMintMarkerRe binds to the `tags=("$TAG" …)` array-literal form; an
//     append form (`tags+=("$TAG")`) would evade the hand-mint ban — but with the
//     marker now sourced from the golden-locked --print-stable-tags, re-hand-minting
//     is a pointless regression the consume check already steers away from.
//   - Only the "Tag modules" step is scanned; the sibling "Validate satellite tag
//     set (stable resume)" step (which re-derives the library tags via
//     --print-tag-paths on resume) is NOT guarded here — its correctness rides on
//     the same modrelease source plus the release-time peel-to-marker-commit check.
//   - A maintainer who renames "Tag modules" or moves the consume to a new step
//     evades the positive check but trips the anti-vacuity tally loudly
//     (require.Positive), not silently.
//
// ref: kubernetes/kubernetes (bare vX.Y.Z = release marker; staging modules tag
//
//	in their own repos) — release-tag-as-marker convention.
//
// ref: golang.org/x/tools gopls (submodule tags as gopls/vX.Y.Z; bare vX.Y.Z is
//
//	the root/release tag) — sub-directory module tag shape.
func TestReleaseStableMarkerTag(t *testing.T) {
	root := findModuleRoot(t)
	// Sanctioned content-scan façade (ai-robust.md §载体选择 rule 4: YAML rules use
	// EachContentFile), matching the sibling archtest_ci_gowork_test.go which audits
	// the same .github/workflows/*.{yml,yaml} set.
	scope := DirsScope(root, []string{filepath.Join(".github", "workflows")})
	total := 0
	EachContentFile(t, scope, []string{".yml", ".yaml"}, func(_ *testing.T, fc ContentContext) {
		matched, verr := validateReleaseStableMarkerTag(fc.Rel, fc.Bytes)
		require.NoError(t, verr)
		total += matched
	})

	// Anti-vacuity: the "Tag modules" step MUST be found in at least one real
	// workflow, or the guard silently passes on everything (step renamed/removed or
	// the stable tagging path restructured).
	require.Positivef(t, total,
		"RELEASE-STABLE-MARKER-TAG-01: no %q step found in any .github/workflows/*.yml — "+
			"step renamed/removed or the stable release tagging path was restructured; the guard "+
			"is vacuous. release.yml must tag the stable release in a %q step.",
		tagModulesStepName, tagModulesStepName)
}

// validateReleaseStableMarkerTag checks one workflow file. Returns the number of
// "Tag modules" steps found (for the caller's anti-vacuity tally) and the first
// violation, if any. A workflow with no such step returns (0, nil) — not an error.
func validateReleaseStableMarkerTag(name string, body []byte) (int, error) {
	var wf markerWorkflow
	if err := yaml.NewDecoder(bytes.NewReader(body)).Decode(&wf); err != nil {
		return 0, fmt.Errorf("RELEASE-STABLE-MARKER-TAG-01: parse %s: %w", name, err)
	}
	matched := 0
	for _, jobName := range markerJobNames(wf.Jobs) {
		for _, step := range wf.Jobs[jobName].Steps {
			if step.Name != tagModulesStepName {
				continue
			}
			matched++
			joined := strings.Join(runCommandLines(step.Run), "\n")
			if !consumeStableTagsRe.MatchString(joined) {
				return matched, fmt.Errorf("RELEASE-STABLE-MARKER-TAG-01: %s job %q step %q does not "+
					"CONSUME modrelease's single-source stable tag set — expected `mapfile -t tags < "+
					"<(go run ./tools/modrelease/cmd --version \"$TAG\" --print-stable-tags)`. Post-#2141 "+
					"modrelease OWNS the bare vX.Y.Z marker (StableTags, byte-locked by "+
					"STABLE-RELEASE-TAG-SET-01); the workflow must consume that golden set, not re-derive "+
					"the library tags and hand-mint the marker. See #2141.", name, jobName, markerStepLabel(step))
			}
			if shellMintMarkerRe.MatchString(joined) {
				return matched, fmt.Errorf("RELEASE-STABLE-MARKER-TAG-01: %s job %q step %q HAND-MINTS the "+
					"bare $TAG marker (`tags=(\"$TAG\" …)`) — post-#2141 the marker is owned by modrelease "+
					"StableTags (Hard golden testdata/stable_tags.golden), NOT shell text. Consume "+
					"--print-stable-tags instead of prepending the marker in the workflow. See #2141.",
					name, jobName, markerStepLabel(step))
			}
			if strings.Contains(joined, staleAssertionMarker) {
				return matched, fmt.Errorf("RELEASE-STABLE-MARKER-TAG-01: %s job %q step %q still "+
					"contains the pre-#1565 assertion %q, which fails every stable release: modrelease's "+
					"--print-tag-paths correctly never emits a bare tag, so requiring it there always exits "+
					"1. Remove it; the marker comes from --print-stable-tags. See #2134/#2141.",
					name, jobName, markerStepLabel(step), staleAssertionMarker)
			}
		}
	}
	return matched, nil
}

func markerStepLabel(s markerStep) string {
	if s.Name != "" {
		return s.Name
	}
	return "(unnamed)"
}

// markerJobNames returns job ids in deterministic order so the first reported
// violation is stable across runs.
func markerJobNames(m map[string]markerJob) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- synthetic fixtures: red (must fail) ---------------------------------

// TestReleaseStableMarkerTag_RejectsHandMintAndStale covers the broken shapes:
// the stable step hand-mints the marker in shell instead of consuming
// --print-stable-tags, consumes only the library-only surface (marker missing),
// or carries the pre-#1565 stale assertion.
func TestReleaseStableMarkerTag_RejectsHandMintAndStale(t *testing.T) {
	cases := map[string]string{
		// Pre-#2141 hand-mint: derives library tags via --print-tag-paths then
		// prepends the bare $TAG in shell. Banned — marker must come from modrelease.
		"hand-mints-marker": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          mapfile -t module_tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-tag-paths)
          tags=("$TAG" "${module_tags[@]}")
          git push origin --atomic "${tags[@]}"
`,
		// Consumes only the library-only --print-tag-paths and never the stable
		// set: the marker is missing entirely (does not consume the single source).
		"no-stable-consume": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          mapfile -t tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-tag-paths)
          git push origin --atomic "${tags[@]}"
`,
		// Consumes the stable set correctly BUT re-introduces the pre-#1565 stale
		// assertion: the regression-lock must still fire independently.
		"stable-consume-but-stale-assertion": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          mapfile -t tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-stable-tags)
          case " ${tags[*]} " in
            *" $TAG "*) ;;
            *) echo "::error::root tag $TAG missing from derived tag set."; exit 1 ;;
          esac
          git push origin --atomic "${tags[@]}"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			matched, err := validateReleaseStableMarkerTag("fixture.yml", []byte(body))
			require.Error(t, err, "hand-mint / missing consume / stale assertion (%s) must be rejected", name)
			require.Positive(t, matched, "fixture must be recognized as a Tag modules step")
		})
	}
}

// --- synthetic fixtures: green (must pass + be non-vacuous) ---------------

func TestReleaseStableMarkerTag_AcceptsStableTagsConsume(t *testing.T) {
	cases := map[string]string{
		"consumes-stable-tags": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          mapfile -t tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-stable-tags)
          if [ "${#tags[@]}" -lt 3 ] || [ "${tags[0]}" != "$TAG" ]; then exit 1; fi
          for tag in "${tags[@]}"; do git tag -a "$tag" -m "Release $tag"; done
          git push origin --atomic "${tags[@]}"
`,
		// Real-shaped: the snapshot branch tags the bare $TAG directly while the
		// stable branch consumes --print-stable-tags. The snapshot `git tag -a "$TAG"`
		// must NOT trip the shell-mint ban (it is not the `tags=("$TAG" …)` form).
		"snapshot-branch-plus-stable-consume": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          if [ "$PRERELEASE" = "true" ]; then
            git tag -a "$TAG" -m "Release $TAG"
            git push origin "$TAG"
            exit 0
          fi
          mapfile -t tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-stable-tags)
          git push origin --atomic "${tags[@]}"
`,
		// Anti-vacuity / false-positive guard: a commented historical mention of the
		// stale assertion AND the old hand-mint must NOT trip the bans (comment-
		// stripping in runCommandLines binds them to actual command text).
		"commented-historical-shapes": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          # historical: we used to "root tag $TAG missing from derived tag set" via tags=("$TAG" ...)
          mapfile -t tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-stable-tags)
          git push origin --atomic "${tags[@]}"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			matched, err := validateReleaseStableMarkerTag("fixture.yml", []byte(body))
			require.NoError(t, err)
			require.Positive(t, matched, "fixture must be recognized as a Tag modules step (non-vacuous)")
		})
	}
}

// A workflow with no "Tag modules" step must return (0, nil): the guard is
// per-file scoped and must not over-match other workflows.
func TestReleaseStableMarkerTag_IgnoresUnrelatedWorkflow(t *testing.T) {
	body := `jobs:
  build:
    steps:
      - name: Build
        run: go build ./...
`
	matched, err := validateReleaseStableMarkerTag("ci.yml", []byte(body))
	require.NoError(t, err, "a workflow without a Tag modules step is out of scope")
	require.Zero(t, matched, "no Tag modules step must not match")
}
