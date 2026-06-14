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

// markerMintRe matches the stable push-set construction that PREPENDS the bare
// $TAG marker to the modrelease-derived library tags:
//
//	tags=("$TAG" "${module_tags[@]}")
//
// Whitespace-tolerant inside the array literal; the leading "$TAG" element is the
// load-bearing assertion — the bare vX.Y.Z GitHub Release marker the workflow
// must mint ITSELF, since modrelease emits only <subdir>/vX.Y.Z library tags
// post-#1565. Comment-stripped command lines are matched (runCommandLines), so a
// commented example cannot satisfy the guard.
var markerMintRe = regexp.MustCompile(`tags=\(\s*"\$TAG"`)

// staleAssertionMarker is the pre-#1565 broken guard's error string: it required
// the bare $TAG to appear in modrelease's derived tag set, which always exits 1
// post-#1565 (modrelease correctly never emits a bare tag). Its reappearance is a
// regression — locked out alongside the positive marker-mint assertion.
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
// .github/workflows/*.y{a,}ml: the stable "Tag modules" step must mint the bare
// $TAG as the GitHub Release marker tag (prepended to the modrelease-derived
// library tag set) and must NOT carry the pre-#1565 "$TAG must be in the derived
// set" assertion.
//
// Why it matters: #1565 moved the core into framework/, so the repo root holds no
// module and modrelease's TagPaths correctly emits only <subdir>/vX.Y.Z library
// tags — never a bare vX.Y.Z (TestTagPaths, modrelease_test.go). But the bare
// vX.Y.Z is the repo-level RELEASE MARKER that `gh release create --verify-tag`,
// goreleaser {{.Tag}}, and the verify-job resume sentinel (`refs/tags/$tag`) all
// resolve against (k8s/gopls convention: bare = release marker, <subdir>/vX = the
// go-get module tag). The pre-#1565 workflow expected modrelease to emit that bare
// tag and asserted its presence — so every stable release dispatch exits 1 (#2134,
// latent: pre-release / develop CI never exercises this path). The fix restores
// the pre-#1565 git ref topology by minting the marker in the workflow; this guard
// keeps that mint from silently regressing.
//
// The deeper purpose is AI-robustness against the #1565→#2134 failure CLASS:
// release semantics living in un-CI-guarded shell that drifts when the module
// layout changes. modrelease's library + installable tag sets are already Hard
// golden-locked (PUBLISHABLE-MODULE-SET-01 / publishable.golden, installable.golden);
// the bare marker was the one release ref with no CI-time guard — its only check,
// `gh release create --verify-tag`, fires at RELEASE time, not in the PR that
// breaks it (the same latency that let #2134 ship green through #2125). This static
// scan pulls the marker invariant into CI.
//
// AI-robust grade: Medium content-scan (machine-checkable; dropping the marker
// mint or re-adding the stale assertion is a reviewer-visible diff that fails this
// test). Hard is reachable but not low-cost — it would require modrelease to own
// the full stable tag set (a `--print-stable-tags` golden the workflow consumes
// verbatim), re-merging the marker into modrelease's surface (in tension with
// "modrelease = pure module tags") + 3 files; tracked as backlog Hard-ization
// issue #2141 per ai-robust.md §审查要求.
//
// Blind spot (disclosed):
//   - This scans the run-block TEXT, not the executed git pushes — it proves the
//     marker is constructed into the push array, not that the push succeeds. Push
//     success / tag-set atomicity is covered at release time by the --atomic push +
//     `gh release create --verify-tag` + the "Validate satellite tag set (stable
//     resume)" step.
//   - markerMintRe binds to the `tags=("$TAG" …)` array-literal form. A maintainer
//     who rewrites the push set into append form (e.g. `tags+=("$TAG")`) would evade
//     the positive match; the Hard-ization (#2141) closes this by deriving the set
//     from modrelease + golden instead of scanning shell text.
//   - Only the "Tag modules" step is scanned; the sibling "Validate satellite tag
//     set (stable resume)" step (which re-derives the library tags on resume) is NOT
//     guarded here — its correctness rides on the same modrelease source plus the
//     release-time peel-to-marker-commit check.
//   - A maintainer who renames "Tag modules" or moves the mint to a new step evades
//     the positive check but trips the anti-vacuity tally loudly (require.Positive),
//     not silently.
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
			if !markerMintRe.MatchString(joined) {
				return matched, fmt.Errorf("RELEASE-STABLE-MARKER-TAG-01: %s job %q step %q does not "+
					"mint the bare $TAG release marker — expected the stable push set to PREPEND it: "+
					"`tags=(\"$TAG\" \"${module_tags[@]}\")`. modrelease emits only <subdir>/vX.Y.Z "+
					"library tags post-#1565, so the workflow must mint the bare vX.Y.Z marker itself; "+
					"it is what `gh release create --verify-tag`, goreleaser {{.Tag}}, and the verify-job "+
					"resume sentinel resolve against. See #2134.", name, jobName, markerStepLabel(step))
			}
			if strings.Contains(joined, staleAssertionMarker) {
				return matched, fmt.Errorf("RELEASE-STABLE-MARKER-TAG-01: %s job %q step %q still "+
					"contains the pre-#1565 assertion %q, which fails every stable release: modrelease "+
					"correctly never emits a bare tag, so requiring it in the derived set always exits 1. "+
					"Remove it; the bare $TAG is minted as the Release marker instead. See #2134.",
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

// TestReleaseStableMarkerTag_RejectsMissingMarker covers the broken shapes: the
// stable step pushes only modrelease's library tags without minting the bare $TAG
// marker, and/or carries the pre-#1565 stale assertion.
func TestReleaseStableMarkerTag_RejectsMissingMarker(t *testing.T) {
	cases := map[string]string{
		// The current (pre-fix) shape: maps modrelease's tags, asserts the bare $TAG
		// is among them, pushes only those — no marker minted.
		"no-marker-with-stale-assertion": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          mapfile -t tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-tag-paths)
          case " ${tags[*]} " in
            *" $TAG "*) ;;
            *) echo "::error::root tag $TAG missing from derived tag set."; exit 1 ;;
          esac
          git push origin --atomic "${tags[@]}"
`,
		// Marker absent even without the stale assertion: still a violation.
		"no-marker": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          mapfile -t module_tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-tag-paths)
          git push origin --atomic "${module_tags[@]}"
`,
		// Marker minted correctly BUT the stale assertion re-introduced: the
		// regression-lock must still fire (the broken case must never reappear).
		"marker-but-stale-assertion": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          mapfile -t module_tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-tag-paths)
          case " ${module_tags[*]} " in
            *" $TAG "*) ;;
            *) echo "::error::root tag $TAG missing from derived tag set."; exit 1 ;;
          esac
          tags=("$TAG" "${module_tags[@]}")
          git push origin --atomic "${tags[@]}"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			matched, err := validateReleaseStableMarkerTag("fixture.yml", []byte(body))
			require.Error(t, err, "missing marker / stale assertion (%s) must be rejected", name)
			require.Positive(t, matched, "fixture must be recognized as a Tag modules step")
		})
	}
}

// --- synthetic fixtures: green (must pass + be non-vacuous) ---------------

func TestReleaseStableMarkerTag_AcceptsMintedMarker(t *testing.T) {
	cases := map[string]string{
		"marker-prepended": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          mapfile -t module_tags < <(go run ./tools/modrelease/cmd --version "$TAG" --print-tag-paths)
          if [ "${#module_tags[@]}" -lt 2 ]; then exit 1; fi
          tags=("$TAG" "${module_tags[@]}")
          for tag in "${tags[@]}"; do git tag -a "$tag" -m "Release $tag"; done
          git push origin --atomic "${tags[@]}"
`,
		// Whitespace tolerance inside the array literal.
		"marker-prepended-spaced": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          tags=(  "$TAG" "${module_tags[@]}")
          git push origin --atomic "${tags[@]}"
`,
		// Anti-vacuity / F3-class false-positive guard: a commented historical
		// mention of the stale assertion must NOT trip the regression-lock
		// (comment-stripping in runCommandLines binds it to actual command text).
		"commented-stale-assertion": `jobs:
  release:
    steps:
      - name: Tag modules
        run: |
          # historical note: we used to assert "root tag $TAG missing from derived tag set"
          tags=("$TAG" "${module_tags[@]}")
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
