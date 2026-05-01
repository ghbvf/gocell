package archtest

import (
	"bytes"
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
)

func TestGolangCILintVersionPinnedToPatch(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "workflows", "_build-lint.yml")))
	require.NoError(t, err)

	re := regexp.MustCompile(`(?m)^\s*version:\s*(v[0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$`)
	matches := re.FindStringSubmatch(string(body))
	require.Len(t, matches, 2, "golangci-lint action version input must be present")
	assert.Regexp(t, regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`), matches[1],
		"golangci-lint must be pinned to patch version, not only major.minor")
}

func TestWorkflowExternalUsesPinnedToSHA(t *testing.T) {
	root := findModuleRoot(t)
	pinPaths, err := pinnableYAMLFiles(root)
	require.NoError(t, err)
	require.NotEmpty(t, pinPaths)

	for _, path := range pinPaths {
		body, readErr := os.ReadFile(filepath.Clean(path))
		require.NoError(t, readErr)
		require.NoError(t, validateWorkflowUsesPinned(path, body))
		require.NoError(t, validateLocalUsesResolve(root, path, body))
	}
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

func TestGeneratedArtifactGatesAreStructured(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "workflows", "_build-lint.yml")))
	require.NoError(t, err)

	require.NoError(t, validateGeneratedArtifactGates(body))
}

func TestGeneratedArtifactGateRejectsProducerDefinedScope(t *testing.T) {
	body := []byte(`jobs:
  build-test:
    steps:
      - name: Verify generated artifacts are up-to-date
        if: matrix.static_checks
        run: |
          go run ./cmd/gocell verify generated
          entrypoints_file="$(mktemp)"
          go run ./cmd/gocell generate assembly --id "$(basename "$d")"
          echo "Generated: cmd/corebundle/main.go"
          generated_entrypoints=()
          while IFS= read -r entrypoint; do
            [ -n "$entrypoint" ] || continue
            generated_entrypoints+=("$entrypoint")
          done < "$entrypoints_file"
          diff_paths=(assemblies/)
          diff_paths+=("${generated_entrypoints[@]}")
          git diff --exit-code -- "${diff_paths[@]}"
          git ls-files --others --exclude-standard -- "${generated_entrypoints[@]}"
          git ls-files --others --exclude-standard assemblies/*/generated/boundary.yaml
`)
	require.Error(t, validateGeneratedArtifactGates(body))
}

func TestGeneratedArtifactGateRejectsLegacyEntrypointGlob(t *testing.T) {
	body := []byte(`jobs:
  build-test:
    steps:
      - name: Verify generated artifacts are up-to-date
        if: matrix.static_checks
        run: |
          go run ./cmd/gocell verify generated
          go run ./cmd/gocell generate assembly --id "$(basename "$d")"
          git diff --exit-code assemblies/ cmd/*/main.go
          git ls-files --others --exclude-standard cmd/*/main.go
          git ls-files --others --exclude-standard assemblies/*/generated/boundary.yaml
`)
	require.Error(t, validateGeneratedArtifactGates(body))
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

type dependabotConfig struct {
	Version int                `yaml:"version"`
	Updates []dependabotUpdate `yaml:"updates"`
}

type dependabotUpdate struct {
	PackageEcosystem string                     `yaml:"package-ecosystem"`
	Directory        string                     `yaml:"directory"`
	Schedule         dependabotSchedule         `yaml:"schedule"`
	Groups           map[string]dependabotGroup `yaml:"groups"`
}

type dependabotSchedule struct {
	Interval string `yaml:"interval"`
}

type dependabotGroup struct {
	Patterns        []string `yaml:"patterns"`
	ExcludePatterns []string `yaml:"exclude-patterns"`
}

func validateDependabotCoversCIAndGolangCILint(body []byte) error {
	var cfg dependabotConfig
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("parse dependabot.yml: %w", err)
	}

	var hasGitHubActions bool
	var hasGoMod bool
	for _, update := range cfg.Updates {
		switch update.PackageEcosystem {
		case "github-actions":
			if update.Directory == "/" {
				hasGitHubActions = true
				if rootGitHubActionsUpdateCoversGolangCI(update) {
					return validateDependabotHasGoModRoot(cfg)
				}
			}
		case "gomod":
			if update.Directory == "/" {
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
		if update.PackageEcosystem == "gomod" && update.Directory == "/" {
			return nil
		}
	}
	return fmt.Errorf("dependabot must update Go module pins from directory /")
}

type workflowConfig struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Uses  string         `yaml:"uses"`
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string `yaml:"name"`
	If   string `yaml:"if"`
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
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

// pinnableYAMLFiles returns every YAML file the SHA-pin and local-reference
// audits must visit. Workflow files (.github/workflows/*.{yml,yaml}) and
// composite/local reusable actions (.github/actions/**/action.{yml,yaml})
// are both in scope: a tag-pinned external action inside a composite
// action would otherwise bypass the pin check because the workflow file
// only lists the local wrapper.
func pinnableYAMLFiles(root string) ([]string, error) {
	var out []string
	workflowsDir := filepath.Join(root, ".github", "workflows")
	for _, ext := range []string{"*.yml", "*.yaml"} {
		paths, err := filepath.Glob(filepath.Join(workflowsDir, ext))
		if err != nil {
			return nil, err
		}
		out = append(out, paths...)
	}
	actionsDir := filepath.Join(root, ".github", "actions")
	if entries, err := os.ReadDir(actionsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			for _, name := range []string{"action.yml", "action.yaml"} {
				candidate := filepath.Join(actionsDir, entry.Name(), name)
				if _, statErr := os.Stat(candidate); statErr == nil {
					out = append(out, candidate)
				}
			}
		}
	}
	return out, nil
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

func validateGeneratedArtifactGates(body []byte) error {
	var cfg workflowConfig
	dec := yaml.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("parse _build-lint.yml: %w", err)
	}
	job, ok := cfg.Jobs["build-test"]
	if !ok {
		return fmt.Errorf("build-test job missing")
	}
	assemblyStep, ok := findWorkflowStep(job.Steps, "Verify generated artifacts are up-to-date")
	if !ok {
		return fmt.Errorf("generated artifact gate missing")
	}
	if err := validateStaticCheckStep(assemblyStep); err != nil {
		return fmt.Errorf("generated artifact gate: %w", err)
	}
	if !strings.Contains(assemblyStep.Run, "go run ./cmd/gocell verify generated") {
		return fmt.Errorf("generated artifact gate must call gocell verify generated")
	}
	for _, forbidden := range []string{
		"Generated:",
		"entrypoints_file",
		"generated_entrypoints",
		"go run ./cmd/gocell generate assembly",
		"go run ./cmd/gocell generate metrics-schema",
		"git diff",
		"git ls-files",
		"cmd/*/main.go",
		"--boundary-only",
	} {
		if strings.Contains(assemblyStep.Run, forbidden) {
			return fmt.Errorf("generated artifact gate must not contain %q", forbidden)
		}
	}
	return nil
}

func findWorkflowStep(steps []workflowStep, name string) (workflowStep, bool) {
	for _, step := range steps {
		if step.Name == name {
			return step, true
		}
	}
	return workflowStep{}, false
}

func validateStaticCheckStep(step workflowStep) error {
	if step.If != "matrix.static_checks" {
		return fmt.Errorf("if must be matrix.static_checks")
	}
	if strings.TrimSpace(step.Run) == "" {
		return fmt.Errorf("run block missing")
	}
	return nil
}
