package archtest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGolangCILintVersionPinnedToPatch(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "_build-lint.yml"))
	require.NoError(t, err)

	re := regexp.MustCompile(`(?m)^\s*version:\s*(v[0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$`)
	matches := re.FindStringSubmatch(string(body))
	require.Len(t, matches, 2, "golangci-lint action version input must be present")
	assert.Regexp(t, regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`), matches[1],
		"golangci-lint must be pinned to patch version, not only major.minor")
}

func TestWorkflowExternalUsesPinnedToSHA(t *testing.T) {
	root := findModuleRoot(t)
	workflowPaths, err := workflowFiles(root)
	require.NoError(t, err)
	require.NotEmpty(t, workflowPaths)

	for _, path := range workflowPaths {
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.NoError(t, validateWorkflowUsesPinned(path, body))
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

func TestWorkflowUsesPinnedAllowsLocalReusableWorkflow(t *testing.T) {
	body := []byte(`jobs:
  test:
    uses: ./.github/workflows/_build-lint.yml
`)
	require.NoError(t, validateWorkflowUsesPinned("fixture.yml", body))
}

func TestGeneratedArtifactGatesAreStructured(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "_build-lint.yml"))
	require.NoError(t, err)

	require.NoError(t, validateGeneratedArtifactGates(body))
}

func TestGeneratedArtifactGateRejectsMissingMetricsUntrackedCheck(t *testing.T) {
	body := []byte(`jobs:
  build-test:
    steps:
      - name: Verify generated assemblies are up-to-date
        if: matrix.static_checks
        run: |
          entrypoints_file="$(mktemp)"
          go run ./cmd/gocell generate assembly --id "$(basename "$d")"
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
      - name: Verify generated metrics-schema is up-to-date
        if: matrix.static_checks
        run: |
          go run ./cmd/gocell generate metrics-schema --id "$(basename "$d")"
          git diff --exit-code assemblies/*/generated/metrics-schema.yaml
`)
	require.Error(t, validateGeneratedArtifactGates(body))
}

func TestGeneratedArtifactGateRejectsLegacyEntrypointGlob(t *testing.T) {
	body := []byte(`jobs:
  build-test:
    steps:
      - name: Verify generated assemblies are up-to-date
        if: matrix.static_checks
        run: |
          go run ./cmd/gocell generate assembly --id "$(basename "$d")"
          git diff --exit-code assemblies/ cmd/*/main.go
          git ls-files --others --exclude-standard cmd/*/main.go
          git ls-files --others --exclude-standard assemblies/*/generated/boundary.yaml
      - name: Verify generated metrics-schema is up-to-date
        if: matrix.static_checks
        run: |
          go run ./cmd/gocell generate metrics-schema --id "$(basename "$d")"
          git diff --exit-code assemblies/*/generated/metrics-schema.yaml
          git ls-files --others --exclude-standard assemblies/*/generated/metrics-schema.yaml
`)
	require.Error(t, validateGeneratedArtifactGates(body))
}

func TestDependabotCoversCIAndGolangCILint(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "dependabot.yml"))
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
		for _, pattern := range group.Patterns {
			if pattern == "golangci/golangci-lint-action" {
				return true
			}
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
			return
		}
		if !shaPinnedAction(uses) {
			violations = append(violations, uses)
		}
	})
	if len(violations) > 0 {
		return fmt.Errorf("%s: external workflow uses must be pinned to 40-char SHA: %s",
			path, strings.Join(violations, ", "))
	}
	return nil
}

func workflowFiles(root string) ([]string, error) {
	var out []string
	for _, ext := range []string{"*.yml", "*.yaml"} {
		paths, err := filepath.Glob(filepath.Join(root, ".github", "workflows", ext))
		if err != nil {
			return nil, err
		}
		out = append(out, paths...)
	}
	return out, nil
}

func walkWorkflowUses(node *yaml.Node, visit func(string)) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			walkWorkflowUses(child, visit)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			value := node.Content[i+1]
			if key.Value == "uses" && value.Kind == yaml.ScalarNode {
				visit(value.Value)
			}
			walkWorkflowUses(value, visit)
		}
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
	assemblyStep, ok := findWorkflowStep(job.Steps, "Verify generated assemblies are up-to-date")
	if !ok {
		return fmt.Errorf("generated assembly gate missing")
	}
	if err := validateStaticCheckStep(assemblyStep); err != nil {
		return fmt.Errorf("generated assembly gate: %w", err)
	}
	for _, want := range []string{
		"go run ./cmd/gocell generate assembly --id",
		"entrypoints_file=",
		"generated_entrypoints=()",
		"git diff --exit-code -- \"${diff_paths[@]}\"",
		"git ls-files --others --exclude-standard -- \"${generated_entrypoints[@]}\"",
		"git ls-files --others --exclude-standard assemblies/*/generated/boundary.yaml",
	} {
		if !strings.Contains(assemblyStep.Run, want) {
			return fmt.Errorf("generated assembly gate missing %q", want)
		}
	}
	if strings.Contains(assemblyStep.Run, "cmd/*/main.go") {
		return fmt.Errorf("generated assembly gate must use metadata entrypoint paths, not cmd/*/main.go")
	}
	if strings.Contains(assemblyStep.Run, "--boundary-only") {
		return fmt.Errorf("generated assembly gate must not use --boundary-only")
	}

	metricsStep, ok := findWorkflowStep(job.Steps, "Verify generated metrics-schema is up-to-date")
	if !ok {
		return fmt.Errorf("metrics-schema gate missing")
	}
	if err := validateStaticCheckStep(metricsStep); err != nil {
		return fmt.Errorf("metrics-schema gate: %w", err)
	}
	for _, want := range []string{
		"go run ./cmd/gocell generate metrics-schema --id",
		"git diff --exit-code assemblies/*/generated/metrics-schema.yaml",
		"git ls-files --others --exclude-standard assemblies/*/generated/metrics-schema.yaml",
	} {
		if !strings.Contains(metricsStep.Run, want) {
			return fmt.Errorf("metrics-schema gate missing %q", want)
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
