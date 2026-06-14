//go:build archtest

// INVARIANT: CI-PINNING-WORKFLOW-DIGEST-01
//   - INVARIANT: DEPENDABOT-COVERAGE-GOLANGCI-01
//
// CI-PINNING-WORKFLOW-DIGEST-01: golangci-lint pinned to patch version; all external workflow uses pinned to SHA
// DEPENDABOT-COVERAGE-GOLANGCI-01: dependabot root github-actions block covers
//
//	golangci/golangci-lint-action and a root gomod block is present; the decode is
//	tolerant of unmodeled orchestration fields (a typo in an asserted field
//	collapses to its zero value and still reds the guard)
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	// patch-pinned (not bare major.minor).
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, "hack", "lib", "golangci-lint.sh")))
	require.NoError(t, err)

	re := regexp.MustCompile(`(?m)^GOLANGCI_LINT_VERSION="(v[0-9]+\.[0-9]+(?:\.[0-9]+)?)"\s*$`)
	matches := re.FindStringSubmatch(string(body))
	require.Len(t, matches, 2, "hack/lib/golangci-lint.sh must declare GOLANGCI_LINT_VERSION")
	assert.Regexp(t, regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`), matches[1],
		"golangci-lint must be pinned to patch version, not only major.minor")
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

func TestDependabotCoversCIAndGolangCILint(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "dependabot.yml")))
	require.NoError(t, err, ".github/dependabot.yml must exist")

	require.NoError(t, validateDependabotCoversCIAndGolangCILint(body))
}

func TestDependabotCoversCIAndGolangCILintRejectsGroupNameOnly(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      golangci-lint:
        patterns:
          - "actions/*"
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
`)
	require.Error(t, validateDependabotCoversCIAndGolangCILint(body),
		"group names must not satisfy the guard unless a pattern covers the action")
}

func TestDependabotCoversCIAndGolangCILintRejectsNonRootPatternOnly(t *testing.T) {
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
  - package-ecosystem: "github-actions"
    directory: "/tools"
    schedule:
      interval: "weekly"
    groups:
      golangci:
        patterns:
          - "golangci/golangci-lint-action"
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
`)
	require.Error(t, validateDependabotCoversCIAndGolangCILint(body),
		"golangci-lint action pattern must be attached to the root github-actions update")
}

func TestDependabotCoversCIAndGolangCILintAllowsGroupExclusions(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      golangci-lint:
        patterns:
          - "golangci/golangci-lint-action"
      github-actions:
        patterns:
          - "*"
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      go-special:
        patterns:
          - "example.com/special/*"
      go-other:
        patterns:
          - "*"
        exclude-patterns:
          - "example.com/special/*"
`)
	require.NoError(t, validateDependabotCoversCIAndGolangCILint(body))
}

// TestDependabotCoversCIAndGolangCILintToleratesUnmodeledFields locks the
// guard's tolerant-evolution contract: dependabot.yml legitimately carries
// orchestration fields the guard does not assert on — ignore (with full
// dependency-name/versions/update-types), open-pull-requests-limit, labels.
// The validator must check only the coverage invariant (root github-actions
// covers golangci + root gomod) and stay tolerant of schema growth. A strict
// KnownFields(true) decode here would
// red on every new dependabot field while adding nothing to the assertion —
// that fragility caused #925 (trigger: #911 added `ignore:`). This test
// prevents a "helpful" reintroduction of strict decode: with strict decode
// re-added, the unmodeled fields below (open-pull-requests-limit / labels /
// ignore + sub-fields) make the decode error and this NoError assertion red.
//
// INVARIANT: DEPENDABOT-COVERAGE-GOLANGCI-01
//
// The fixture MUST retain those unmodeled fields — they are the regression
// trip-wire; shrinking the fixture to only modeled fields would silently
// turn this test into a tautology that no longer catches strict-decode reentry.
func TestDependabotCoversCIAndGolangCILintToleratesUnmodeledFields(t *testing.T) {
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
      golangci-lint:
        patterns:
          - "golangci/golangci-lint-action"
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
	require.NoError(t, validateDependabotCoversCIAndGolangCILint(body))
}

// TestDependabotCoversCIAndGolangCILintRejectsGroupsFieldTypo is the
// blind-spot self-check for dropping strict decode (#925): a typo in an
// *asserted* field must still red the guard. Here `groops:` (typo of
// `groups:`) is tolerantly ignored, leaving Groups empty, so the root
// github-actions block can no longer cover golangci/golangci-lint-action and
// the guard reds. This proves tolerant decode stays fail-closed on the fields
// the guard reads — the safety claim that justified removing KnownFields(true).
//
// INVARIANT: DEPENDABOT-COVERAGE-GOLANGCI-01 — groups-field typo test.
func TestDependabotCoversCIAndGolangCILintRejectsGroupsFieldTypo(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    groops:
      golangci-lint:
        patterns:
          - "golangci/golangci-lint-action"
  - package-ecosystem: "gomod"
    directory: "/"
`)
	require.Error(t, validateDependabotCoversCIAndGolangCILint(body),
		"a typo in the asserted `groups` field must red the guard, not pass silently")
}

// TestDependabotCoversCIAndGolangCILintAcceptsDirectoriesList locks the guard's
// support for dependabot's plural `directories:` list form. dependabot natively
// supports both singular `directory:` (one dir) and plural `directories:` (a
// list / glob); the real .github/dependabot.yml uses the plural form for its
// gomod block to cover the workspace's many sub-modules. The guard must detect
// root ("/") coverage in EITHER form — modeling only `directory:` made the
// gomod root check silently fail (#2113). Anti-vacuity companion below proves
// the plural path is not a tautology.
//
// INVARIANT: DEPENDABOT-COVERAGE-GOLANGCI-01 — plural directories list form.
func TestDependabotCoversCIAndGolangCILintAcceptsDirectoriesList(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      golangci-lint:
        patterns:
          - "golangci/golangci-lint-action"
  - package-ecosystem: "gomod"
    directories:
      - "/"
      - "/tools"
    schedule:
      interval: "weekly"
`)
	require.NoError(t, validateDependabotCoversCIAndGolangCILint(body),
		"plural `directories:` list containing / must satisfy the root gomod coverage check")
}

// TestDependabotCoversCIAndGolangCILintRejectsDirectoriesListWithoutRoot is the
// anti-vacuity red case for the plural form: a `directories:` list that omits
// "/" must NOT satisfy the root gomod coverage check, proving the plural-path
// check tests membership of "/" rather than mere presence of the field.
//
// INVARIANT: DEPENDABOT-COVERAGE-GOLANGCI-01 — plural directories without root.
func TestDependabotCoversCIAndGolangCILintRejectsDirectoriesListWithoutRoot(t *testing.T) {
	body := []byte(`version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
    groups:
      golangci-lint:
        patterns:
          - "golangci/golangci-lint-action"
  - package-ecosystem: "gomod"
    directories:
      - "/tools"
      - "/cmd/gocell"
    schedule:
      interval: "weekly"
`)
	require.Error(t, validateDependabotCoversCIAndGolangCILint(body),
		"a plural `directories:` list without / must not satisfy the root gomod coverage check")
}

// dependabotConfig models only the fields validateDependabotCoversCIAndGolangCILint
// asserts on. The decode is intentionally tolerant (no KnownFields(true)):
// dependabot.yml legitimately grows orchestration fields (ignore /
// open-pull-requests-limit / labels / reviewers / …) that the coverage guard
// does not care about, and strict decode would red on each one while adding
// nothing to the assertion (a typo in a field the guard *does* read collapses
// it to its zero value, so the pattern match fails and the guard reds anyway).
// The tolerant-decode contract is locked by the fixtures above
// (ToleratesUnmodeledFields + RejectsGroupsFieldTypo). See #925.
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
// group's Patterns contains the required action, never what is excluded, so
// exclude-patterns is left unmodeled and tolerantly ignored (see the
// AllowsGroupExclusions fixture).
type dependabotGroup struct {
	Patterns []string `yaml:"patterns"`
}

func validateDependabotCoversCIAndGolangCILint(body []byte) error {
	var cfg dependabotConfig
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return fmt.Errorf("parse dependabot.yml: %w", err)
	}

	var hasGitHubActions bool
	var hasGoMod bool
	for _, update := range cfg.Updates {
		switch update.PackageEcosystem {
		case "github-actions":
			if update.coversRoot() {
				hasGitHubActions = true
				if rootGitHubActionsUpdateCoversGolangCI(update) {
					return validateDependabotHasGoModRoot(cfg)
				}
			}
		case "gomod":
			if update.coversRoot() {
				hasGoMod = true
			}
		}
	}
	if !hasGitHubActions {
		return fmt.Errorf("dependabot must update GitHub Actions pins from directory /")
	}
	if !hasGoMod {
		return fmt.Errorf("dependabot must update Go module pins from directory /")
	}
	return fmt.Errorf("dependabot root github-actions groups must explicitly pattern-match golangci/golangci-lint-action")
}

func rootGitHubActionsUpdateCoversGolangCI(update dependabotUpdate) bool {
	for _, group := range update.Groups {
		if slices.Contains(group.Patterns, "golangci/golangci-lint-action") {
			return true
		}
	}
	return false
}

func validateDependabotHasGoModRoot(cfg dependabotConfig) error {
	for _, update := range cfg.Updates {
		if update.PackageEcosystem == "gomod" && update.coversRoot() {
			return nil
		}
	}
	return fmt.Errorf("dependabot must update Go module pins from directory /")
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
