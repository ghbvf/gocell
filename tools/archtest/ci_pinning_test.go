package archtest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

func TestGeneratedBoundaryGateChecksUntrackedFiles(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "_build-lint.yml"))
	require.NoError(t, err)
	text := string(body)

	assert.Contains(t, text, "go run ./cmd/gocell generate assembly --id \"$(basename \"$d\")\" --boundary-only")
	assert.Contains(t, text, "git ls-files --others --exclude-standard assemblies/*/generated/boundary.yaml")
	assert.Contains(t, text, "Untracked boundary.yaml files found")
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
	Patterns []string `yaml:"patterns"`
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
