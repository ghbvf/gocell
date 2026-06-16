//go:build archtest

// INVARIANT: CI-PINNING-WORKFLOW-DIGEST-01
//   - INVARIANT: DEPENDABOT-NO-GHOST-GOLANGCI-01
//
// CI-PINNING-WORKFLOW-DIGEST-01: golangci-lint pinned to patch version; all external workflow uses pinned to SHA
// DEPENDABOT-NO-GHOST-GOLANGCI-01: dependabot must NOT contain any group whose patterns include
//
//	"golangci/golangci-lint-action" — that action was removed in #1565/#2125 and its
//	dependabot group is a ghost that implies false auto-update coverage of the real lint
//	pin. The real pin lives in hack/lib/golangci-lint.sh::GOLANGCI_LINT_VERSION (a
//	`go install @version` shell literal, absent from any go.mod); version upgrades are
//	manual per the accepted strategy in #2160; patch-pinning of the constant is
//	archtest-guarded by CI-PINNING-WORKFLOW-DIGEST-01. This guard prevents an AI
//	collaborator from "helpfully" re-adding the ghost group. Additionally, dependabot
//	must still contain a root github-actions block and a root gomod block.
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestGolangCILintVersionPinnedToPatch(t *testing.T) {
	root := findModuleRoot(t)
	// Since #1565 (PR #2125) golangci-lint runs via the shared funnel — the
	// golangci-lint-action `version:` input in _build-lint.yml was removed and
	// the pin now lives solely in hack/lib/golangci-lint.sh::GOLANGCI_LINT_VERSION
	// (resolved by gocell::golangci_lint::ensure). That constant is the single
	// source of the CI lint pin; CI-PINNING-WORKFLOW-DIGEST-01 guards it stays
	// patch-pinned (not bare major.minor) AND is declared exactly once.
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, "hack", "lib", "golangci-lint.sh")))
	require.NoError(t, err)
	require.NoError(t, validateGolangCILintPatchPinned(body))
}

// validateGolangCILintPatchPinned checks that the golangci-lint pin source
// declares GOLANGCI_LINT_VERSION exactly once and pins it to a full patch
// version (vMAJOR.MINOR.PATCH), not a bare vMAJOR.MINOR.
//
// Exactly-once is load-bearing: hack/lib/golangci-lint.sh is `source`d by every
// consumer (CI lint step, make fmt, pre-push hook), and shell keeps the LAST
// assignment of a variable. A first-match-only check (the pre-#2158 form used
// regexp.FindStringSubmatch) read only the topmost line, so appending a second
// `GOLANGCI_LINT_VERSION="v2.12"` below the patch-pinned one would make the
// unpinned major.minor value win at runtime while the guard stayed green
// (#2158 F2). The guard therefore rejects duplicate declarations outright; the
// assignment regex captures ANY quoted value so a laundered second assignment
// is still counted, not silently skipped.
func validateGolangCILintPatchPinned(body []byte) error {
	assign := regexp.MustCompile(`(?m)^GOLANGCI_LINT_VERSION="([^"]*)"\s*$`)
	all := assign.FindAllStringSubmatch(string(body), -1)
	if len(all) != 1 {
		return fmt.Errorf("hack/lib/golangci-lint.sh must declare GOLANGCI_LINT_VERSION "+
			"exactly once (shell `source` keeps the last assignment); found %d", len(all))
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(all[0][1]) {
		return fmt.Errorf("golangci-lint must be pinned to patch version "+
			"(vMAJOR.MINOR.PATCH), not only major.minor; got %q", all[0][1])
	}
	return nil
}

// TestValidateGolangCILintPatchPinned is the synthetic red/green table for the
// patch-pin guard. The real hack/lib/golangci-lint.sh declares the version
// exactly once, so the duplicate-assignment regression (#2158 F2) can only be
// exercised against fixtures here.
//
// INVARIANT: CI-PINNING-WORKFLOW-DIGEST-01 — exactly-once patch pin.
func TestValidateGolangCILintPatchPinned(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"single patch pin", "GOLANGCI_LINT_VERSION=\"v2.11.4\"\n", false},
		{"bare major.minor", "GOLANGCI_LINT_VERSION=\"v2.11\"\n", true},
		{"no declaration", "echo hi\n", true},
		// shell `source` keeps the LAST assignment: a second bare major.minor
		// below the pinned line wins at runtime, so the guard must red on the
		// duplicate rather than read only the first (the #2158 F2 hole).
		{"duplicate, second unpinned", "GOLANGCI_LINT_VERSION=\"v2.11.4\"\nGOLANGCI_LINT_VERSION=\"v2.12\"\n", true},
		// even two patch-pinned assignments are an ambiguous single-source — reject.
		{"duplicate, both patch-pinned", "GOLANGCI_LINT_VERSION=\"v2.11.4\"\nGOLANGCI_LINT_VERSION=\"v2.11.5\"\n", true},
		// a non-version garbage value is still a (rejected) declaration, not skipped.
		{"single garbage value", "GOLANGCI_LINT_VERSION=\"latest\"\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGolangCILintPatchPinned([]byte(tt.body))
			if tt.wantErr {
				require.Error(t, err, "expected validation error for %q", tt.body)
			} else {
				require.NoError(t, err, "expected validation pass for %q", tt.body)
			}
		})
	}
}

func TestWorkflowExternalUsesPinnedToSHA(t *testing.T) {
	root := findModuleRoot(t)
	scope := pinnableYAMLScope(root)
	hits := 0
	scanner.EachContentFile(t, scope, []string{".yml", ".yaml"}, func(_ *testing.T, fc scanner.ContentContext) {
		hits++
		require.NoError(t, validateWorkflowUsesPinned(fc.AbsPath, fc.Bytes))
		require.NoError(t, validateLocalUsesResolve(root, fc.AbsPath, fc.Bytes))
	})
	require.Greater(t, hits, 0, "no pinnable YAML files found — expected at least one workflow or action")
}

func TestWorkflowUsesPinnedRejectsTagPinnedAction(t *testing.T) {
	body := []byte(`jobs:
  test:
    steps:
      - uses: actions/checkout@v6
`)
	require.Error(t, validateWorkflowUsesPinned("fixture.yml", body))
}

func TestWorkflowUsesPinnedRejectsTagPinnedActionThroughAlias(t *testing.T) {
	body := []byte(`x-actions:
  checkout: &checkout actions/checkout@v6
jobs:
  test:
    steps:
      - uses: *checkout
`)
	require.Error(t, validateWorkflowUsesPinned("fixture.yml", body))
}

func TestWorkflowUsesPinnedAllowsLocalReusableWorkflow(t *testing.T) {
	body := []byte(`jobs:
  test:
    uses: ./.github/workflows/_build-lint.yml
`)
	require.NoError(t, validateWorkflowUsesPinned("fixture.yml", body))
}

// TestWorkflowUsesPinnedRejectsTagPinnedActionInsideCompositeAction guards the
// completeness gap from the PR #332 round-2 review: composite actions
// declared at .github/actions/<name>/action.yml are not in the workflow
// glob, so a composite action that pulls actions/checkout@v6 internally
// would historically slip past the pin check. validateWorkflowUsesPinned
// is YAML-shape agnostic and should reject the violation regardless of
// where the file lives; pinnableYAMLFiles puts action.yml files in scope
// so the assertion runs against composite actions too.
func TestWorkflowUsesPinnedRejectsTagPinnedActionInsideCompositeAction(t *testing.T) {
	body := []byte(`name: composite-fixture
runs:
  using: composite
  steps:
    - uses: actions/checkout@v6
      shell: bash
      run: echo hi
`)
	require.Error(t, validateWorkflowUsesPinned("fixture/action.yml", body))
}

// TestWorkflowUsesPinnedRejectsDockerActionWithoutDigest covers the second
// completeness gap: docker:// uses are pinned by sha256 digest, not by a
// 40-hex git SHA. shaPinnedAction's regex never matches digests, so any
// docker:// reference must be checked separately. Allow only the explicit
// digest form so a tag-pinned docker action does not slip through.
func TestWorkflowUsesPinnedRejectsDockerActionWithoutDigest(t *testing.T) {
	body := []byte(`jobs:
  test:
    steps:
      - uses: docker://alpine:3.20
`)
	require.Error(t, validateWorkflowUsesPinned("fixture.yml", body))
}

func TestWorkflowUsesPinnedAcceptsDockerActionWithDigest(t *testing.T) {
	body := []byte(`jobs:
  test:
    steps:
      - uses: docker://alpine@sha256:1c4eef651f65e2f7daee7ee785882ac164b02b78fb74503052a26dc061c90474
`)
	require.NoError(t, validateWorkflowUsesPinned("fixture.yml", body))
}

func TestValidateLocalUsesResolveRejectsMissingTarget(t *testing.T) {
	root := t.TempDir()
	body := []byte(`jobs:
  test:
    uses: ./.github/workflows/missing.yml
`)
	require.Error(t, validateLocalUsesResolve(root, "fixture.yml", body))
}

func TestValidateLocalUsesResolveAcceptsExistingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".github", "workflows", "_local.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("jobs: {}\n"), 0o644))

	body := []byte(`jobs:
  test:
    uses: ./.github/workflows/_local.yml
`)
	require.NoError(t, validateLocalUsesResolve(root, "fixture.yml", body))
}

// TestDependabotNoGhostGolangCILint reads the real .github/dependabot.yml and
// verifies the new DEPENDABOT-NO-GHOST-GOLANGCI-01 invariant: no group must
// cover golangci/golangci-lint-action (removed in #1565/#2125), and both a
// root github-actions block and a root gomod block must be present.
//
// INVARIANT: DEPENDABOT-NO-GHOST-GOLANGCI-01.
func TestDependabotNoGhostGolangCILint(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "dependabot.yml")))
	require.NoError(t, err, ".github/dependabot.yml must exist")

	require.NoError(t, validateDependabotNoGhostGolangCILint(body))
}

// TestDependabotNoGhostGolangCILintRejectsGhostGroup is the anti-vacuity red
// case for the ghost-action ban. Any group whose patterns contain
// "golangci/golangci-lint-action" must be rejected, regardless of which
// ecosystem or directory the update belongs to.
//
// INVARIANT: DEPENDABOT-NO-GHOST-GOLANGCI-01 — ghost group ban anti-vacuity.
func TestDependabotNoGhostGolangCILintRejectsGhostGroup(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      github-actions:
        patterns:
          - "*"
      golangci-lint:
        patterns:
          - "golangci/golangci-lint-action"
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
`)
	require.Error(t, validateDependabotNoGhostGolangCILint(body),
		"any group whose patterns include golangci/golangci-lint-action must be rejected")
}

// TestDependabotNoGhostGolangCILintToleratesUnmodeledFields locks the guard's
// tolerant-evolution contract: dependabot.yml legitimately carries orchestration
// fields the guard does not assert on — ignore (with full dependency-name /
// versions / update-types), open-pull-requests-limit, labels. The validator must
// check only the ghost-ban + root-coverage invariant and stay tolerant of schema
// growth. A strict KnownFields(true) decode here would red on every new dependabot
// field while adding nothing to the assertion — that fragility caused #925
// (trigger: #911 added `ignore:`). This test prevents a "helpful" reintroduction
// of strict decode: with strict decode re-added, the unmodeled fields below
// (open-pull-requests-limit / labels / ignore + sub-fields) make the decode error
// and this NoError assertion red.
//
// INVARIANT: DEPENDABOT-NO-GHOST-GOLANGCI-01 — tolerant decode contract.
//
// The fixture MUST retain those unmodeled fields — they are the regression
// trip-wire; shrinking the fixture to only modeled fields would silently turn
// this test into a tautology that no longer catches strict-decode reentry.
func TestDependabotNoGhostGolangCILintToleratesUnmodeledFields(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    open-pull-requests-limit: 5
    labels:
      - "dependencies"
    groups:
      github-actions:
        patterns:
          - "*"
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
    ignore:
      - dependency-name: "mvdan.cc/gofumpt"
        versions:
          - "0.10.0"
        update-types:
          - "version-update:semver-minor"
    groups:
      go-other:
        patterns:
          - "*"
`)
	require.NoError(t, validateDependabotNoGhostGolangCILint(body))
}

// TestDependabotNoGhostGolangCILintRejectsDirectoriesListWithoutRoot is the
// anti-vacuity red case for the plural directories form: a `directories:` list
// that omits "/" must NOT satisfy the root gomod coverage check, proving the
// plural-path check tests membership of "/" rather than mere presence of the field.
//
// INVARIANT: DEPENDABOT-NO-GHOST-GOLANGCI-01 — plural directories without root.
func TestDependabotNoGhostGolangCILintRejectsDirectoriesListWithoutRoot(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      github-actions:
        patterns:
          - "*"
  - package-ecosystem: "gomod"
    directories:
      - "/tools"
      - "/cmd/gocell"
    schedule:
      interval: "weekly"
`)
	require.Error(t, validateDependabotNoGhostGolangCILint(body),
		"a plural `directories:` list without / must not satisfy the root gomod coverage check")
}

// TestDependabotNoGhostGolangCILintRejectsAssertedFieldTypo is the blind-spot
// self-check for dropping strict decode (#925): a typo in an *asserted* field
// must still red the guard. Here `groops:` (typo of `groups:`) is tolerantly
// ignored, leaving Groups empty for the github-actions update, so the guard
// cannot verify the absence of ghost patterns — but more critically, with the
// empty Groups map the gomod root check still passes (gomod block has no groups
// with ghost patterns). The real failure here is the github-actions root block
// itself: since there are no groups at all on it, the YAML decode collapses
// groups to nil, which means the guard correctly sees no ghost. But the typo
// in the gomod `directory:` field (spelled `directori:`) collapses to empty
// string, so coversRoot() returns false → hasGoMod=false → guard reds.
// This proves tolerant decode stays fail-closed on the fields the guard reads.
//
// INVARIANT: DEPENDABOT-NO-GHOST-GOLANGCI-01 — asserted-field typo test.
func TestDependabotNoGhostGolangCILintRejectsAssertedFieldTypo(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      github-actions:
        patterns:
          - "*"
  - package-ecosystem: "gomod"
    directori: "/"
    schedule:
      interval: "weekly"
`)
	require.Error(t, validateDependabotNoGhostGolangCILint(body),
		"a typo in the asserted `directory` field must red the guard, not pass silently")
}

// dependabotConfig models only the fields validateDependabotNoGhostGolangCILint
// asserts on. The decode is intentionally tolerant (no KnownFields(true)):
// dependabot.yml legitimately grows orchestration fields (ignore /
// open-pull-requests-limit / labels / reviewers / …) that the coverage guard
// does not care about, and strict decode would red on each one while adding
// nothing to the assertion (a typo in a field the guard *does* read collapses
// it to its zero value, so the check fails and the guard reds anyway).
// The tolerant-decode contract is locked by the fixtures above
// (ToleratesUnmodeledFields + RejectsAssertedFieldTypo). See #925.
type dependabotConfig struct {
	Updates []dependabotUpdate `yaml:"updates"`
}

type dependabotUpdate struct {
	PackageEcosystem string                     `yaml:"package-ecosystem"`
	Directory        string                     `yaml:"directory"`
	Directories      []string                   `yaml:"directories"`
	Groups           map[string]dependabotGroup `yaml:"groups"`
}

// coversRoot reports whether the update targets the repo root ("/"). dependabot
// natively supports BOTH the singular `directory:` field (one dir — used by the
// github-actions / docker blocks) and the plural `directories:` list (a list /
// glob — used by the gomod block to cover the workspace's many sub-modules), so
// the guard must accept either form. Modeling only `directory:` made the gomod
// root check silently fail once dependabot.yml moved gomod to the list form
// (#2113). Both branches are load-bearing — they model dependabot's real schema
// union, not a legacy/new compat shim.
func (u dependabotUpdate) coversRoot() bool {
	return u.Directory == "/" || slices.Contains(u.Directories, "/")
}

// dependabotGroup deliberately models only Patterns. The guard asks whether a
// group's Patterns contains the ghost action, never what is excluded, so
// exclude-patterns is left unmodeled and tolerantly ignored.
type dependabotGroup struct {
	Patterns []string `yaml:"patterns"`
}

// anyGroupCoversGhost reports whether any group in the given update has a
// pattern matching the removed golangci/golangci-lint-action. This is used to
// detect ghost coverage — the action was removed in #1565/#2125 so any
// dependabot group covering it creates a misleading impression that the real
// lint pin (hack/lib/golangci-lint.sh) is auto-updated.
func anyGroupCoversGhost(update dependabotUpdate) bool {
	for _, group := range update.Groups {
		if slices.Contains(group.Patterns, "golangci/golangci-lint-action") {
			return true
		}
	}
	return false
}

// validateDependabotNoGhostGolangCILint enforces DEPENDABOT-NO-GHOST-GOLANGCI-01:
//  1. No update group (in any ecosystem or directory) must pattern-match
//     "golangci/golangci-lint-action" — that action is a ghost since #1565/#2125.
//     The real lint pin is hack/lib/golangci-lint.sh::GOLANGCI_LINT_VERSION (manual
//     bump per #2160; patch-pin guarded by CI-PINNING-WORKFLOW-DIGEST-01).
//  2. A root github-actions update (directory "/") must exist.
//  3. A root gomod update (directory "/" or directories list containing "/") must exist.
func validateDependabotNoGhostGolangCILint(body []byte) error {
	var cfg dependabotConfig
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return fmt.Errorf("parse dependabot.yml: %w", err)
	}

	var hasGitHubActionsRoot bool
	var hasGoModRoot bool

	for _, update := range cfg.Updates {
		// Ghost ban: reject any group covering the removed action, regardless of ecosystem.
		if anyGroupCoversGhost(update) {
			return fmt.Errorf("dependabot.yml must not contain a group with pattern " +
				"\"golangci/golangci-lint-action\": that action was removed in " +
				"#1565/#2125; the real lint pin is hack/lib/golangci-lint.sh " +
				"(manual bump per #2160, patch-pin by CI-PINNING-WORKFLOW-DIGEST-01); " +
				"remove the ghost group to prevent false auto-update coverage")
		}

		switch update.PackageEcosystem {
		case "github-actions":
			if update.coversRoot() {
				hasGitHubActionsRoot = true
			}
		case "gomod":
			if update.coversRoot() {
				hasGoModRoot = true
			}
		}
	}

	if !hasGitHubActionsRoot {
		return fmt.Errorf("dependabot must have a github-actions update covering directory /")
	}
	if !hasGoModRoot {
		return fmt.Errorf("dependabot must have a gomod update covering directory /")
	}
	return nil
}

func validateWorkflowUsesPinned(path string, body []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(body, &root); err != nil {
		return fmt.Errorf("%s: parse workflow: %w", path, err)
	}
	var violations []string
	walkWorkflowUses(&root, func(uses string) {
		if strings.HasPrefix(uses, "./") {
			// Local references are resolved by validateLocalUsesResolve;
			// pin-checking them here would make no sense (they have no
			// SHA), and skipping them silently was the original PR #332
			// gap. The explicit split keeps each predicate focused.
			return
		}
		if strings.HasPrefix(uses, "docker://") {
			if !dockerDigestPinned(uses) {
				violations = append(violations, uses)
			}
			return
		}
		if !shaPinnedAction(uses) {
			violations = append(violations, uses)
		}
	})
	if len(violations) > 0 {
		return fmt.Errorf("%s: external workflow uses must be pinned to a 40-char SHA "+
			"(docker:// must use sha256 digest): %s",
			path, strings.Join(violations, ", "))
	}
	return nil
}

// validateLocalUsesResolve reports any `uses: ./...` reference whose target
// file does not exist relative to repoRoot. Local references are scoped to
// the repo, so a typo or stale reference must fail loudly rather than be
// treated as "exempt from SHA pinning".
func validateLocalUsesResolve(repoRoot, path string, body []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(body, &root); err != nil {
		return fmt.Errorf("%s: parse workflow: %w", path, err)
	}
	var missing []string
	walkWorkflowUses(&root, func(uses string) {
		if !strings.HasPrefix(uses, "./") {
			return
		}
		// uses values are POSIX-style; convert to native path separators
		// before joining so the check works on Windows (defensive — CI
		// lives on Linux today, but the cost is one filepath.FromSlash).
		target := filepath.Join(repoRoot, filepath.FromSlash(strings.TrimPrefix(uses, "./")))
		if _, err := os.Stat(target); err != nil {
			missing = append(missing, uses)
		}
	})
	if len(missing) > 0 {
		return fmt.Errorf("%s: local `uses:` references point to missing files: %s",
			path, strings.Join(missing, ", "))
	}
	return nil
}

// pinnableYAMLScope returns the scope covering every YAML file the SHA-pin
// and local-reference audits must visit. Workflow files
// (.github/workflows/*.{yml,yaml}) and composite/local reusable actions
// (.github/actions/<name>/action.{yml,yaml}) are both in scope: a tag-pinned
// external action inside a composite action would otherwise bypass the pin
// check because the workflow file only lists the local wrapper.
//
// Implementation: DirsScope rooted at .github with a MatchRels predicate
// expressing the union of the two file shapes. Composite-action scope is
// fixed at depth 3 (.github/actions/<name>/action.yml) — nested action.yml
// files in deeper sub-directories are ignored, matching the original walk.
func pinnableYAMLScope(root string) scanner.Scope {
	return scanner.DirsScope(
		root, []string{".github"},
		scanner.MatchRels(func(rel string) bool {
			rel = filepath.ToSlash(rel)
			parts := strings.Split(rel, "/")
			if len(parts) < 2 {
				return false
			}
			// Workflow file: .github/workflows/<name>.{yml,yaml}
			if len(parts) == 3 && parts[1] == "workflows" {
				return true
			}
			// Composite action: .github/actions/<name>/action.{yml,yaml}
			if len(parts) == 4 && parts[1] == "actions" {
				return parts[3] == "action.yml" || parts[3] == "action.yaml"
			}
			return false
		}),
	)
}

func walkWorkflowUses(node *yaml.Node, visit func(string)) {
	walkWorkflowUsesSeen(node, visit, map[*yaml.Node]bool{})
}

func walkWorkflowUsesSeen(node *yaml.Node, visit func(string), seen map[*yaml.Node]bool) {
	if node == nil {
		return
	}
	if seen[node] {
		return
	}
	seen[node] = true
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			walkWorkflowUsesSeen(child, visit, seen)
		}
	case yaml.AliasNode:
		walkWorkflowUsesSeen(node.Alias, visit, seen)
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			value := node.Content[i+1]
			if key.Value == "uses" {
				if uses, ok := workflowScalarValue(value, map[*yaml.Node]bool{}); ok {
					visit(uses)
				}
			}
			walkWorkflowUsesSeen(value, visit, seen)
		}
	}
}

func workflowScalarValue(node *yaml.Node, seen map[*yaml.Node]bool) (string, bool) {
	if node == nil || seen[node] {
		return "", false
	}
	seen[node] = true
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Value, true
	case yaml.AliasNode:
		return workflowScalarValue(node.Alias, seen)
	default:
		return "", false
	}
}

func shaPinnedAction(uses string) bool {
	at := strings.LastIndex(uses, "@")
	if at < 0 || at == len(uses)-1 {
		return false
	}
	sha := uses[at+1:]
	return regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(sha)
}

// dockerDigestPinned reports whether a docker:// uses entry pins the image
// to a sha256 digest. Format: `docker://image[:tag]@sha256:<64 hex>`. Tag
// pinning alone (`docker://image:tag`) is not pinning — the tag can be
// repointed to a different image without changing the workflow.
func dockerDigestPinned(uses string) bool {
	const prefix = "docker://"
	rest := strings.TrimPrefix(uses, prefix)
	at := strings.LastIndex(rest, "@")
	if at < 0 || at == len(rest)-1 {
		return false
	}
	digest := rest[at+1:]
	return regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(digest)
}
