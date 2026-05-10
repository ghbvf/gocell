// INVARIANT: CI-INTEGRATION-DISCOVERY-01: integration-test step uses `go list -tags=integration` discovery, not hardcoded globs
package archtest

import (
	"bufio"
	"bytes"
	"go/build/constraint"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// discoverPackagesUnderTag walks rootDir and returns relative paths of every
// directory containing at least one .go file whose //go:build expression
// evaluates true under {tag} alone AND false under no tags.
//
// "{tag} alone" matches the visibility a `go list -tags=<tag>` invocation
// would produce: a file with `integration && otelcollector` is NOT discovered
// because that expression is false under {integration} alone. This mirrors
// the carve-out semantics of the dedicated OTel/race CI steps.
func discoverPackagesUnderTag(rootDir, tag string) ([]string, error) {
	seen := map[string]bool{}
	var pkgs []string

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		ok, parseErr := fileHasExclusivelyTag(path, tag)
		if parseErr != nil {
			return parseErr
		}
		if !ok {
			return nil
		}
		dir := filepath.Dir(path)
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		rel, relErr := filepath.Rel(rootDir, dir)
		if relErr != nil {
			rel = dir
		}
		pkgs = append(pkgs, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(pkgs)
	return pkgs, nil
}

// fileHasExclusivelyTag returns true iff the file's header //go:build line
// evaluates true under {tag} and false under {} — i.e., the file IS gated on
// the tag and would be visible to `go list -tags=<tag>`.
//
// Compound expressions like `integration && otelcollector` evaluate false
// under {integration} alone, so files using such constraints are NOT
// returned for tag="integration". They belong to dedicated CI steps
// (OTel smoke, integration_cluster vet, etc.) and must not be confused
// with the main integration-test gate.
func fileHasExclusivelyTag(path, tag string) (bool, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			if !constraint.IsGoBuild(line) {
				continue
			}
			expr, parseErr := constraint.Parse(line)
			if parseErr != nil {
				return false, parseErr
			}
			withTag := expr.Eval(func(t string) bool { return t == tag })
			withoutAny := expr.Eval(func(_ string) bool { return false })
			return withTag && !withoutAny, nil
		}
		break
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// readIntegrationTestStep reads _build-lint.yml and returns the
// "Integration tests (testcontainers)" step from the integration-test job.
// Step / job names are coupled to the workflow file by string; renaming
// either is a coordinated change with this archtest.
func readIntegrationTestStep(t *testing.T) workflowStep {
	t.Helper()
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "workflows", "_build-lint.yml")))
	require.NoError(t, err)

	var cfg workflowConfig
	require.NoError(t, yaml.NewDecoder(bytes.NewReader(body)).Decode(&cfg))

	job, ok := cfg.Jobs["integration-test"]
	require.True(t, ok, "integration-test job missing from _build-lint.yml")

	step, ok := findWorkflowStep(job.Steps, "Integration tests (testcontainers)")
	require.True(t, ok, "Integration tests (testcontainers) step missing from integration-test job")
	return step
}

// TestArchtest_CIIntegrationDiscovery_DiscoversIntegrationPackages asserts
// the static walker finds a non-trivial number of packages with files gated
// under -tags=integration. The lower bound (10) is a sanity check: the live
// count is ~17 in develop. A bump-down here without coordinated CI surgery
// signals the regression class CI-INTEGRATION-DISCOVERY-01 protects against.
func TestArchtest_CIIntegrationDiscovery_DiscoversIntegrationPackages(t *testing.T) {
	root := findModuleRoot(t)
	pkgs, err := discoverPackagesUnderTag(root, "integration")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(pkgs), 10,
		"expected ≥10 integration packages discovered, got %d: %v", len(pkgs), pkgs)
	t.Logf("discovered %d integration packages", len(pkgs))
}

// TestArchtest_CIIntegrationDiscovery_DiscoversE2EPackages asserts at least
// one e2e package is found. e2e tag is rarer (mostly tests/e2e/...) but
// must never be empty — an empty set means the walker is broken or the e2e
// surface vanished, both of which warrant fail-loud.
func TestArchtest_CIIntegrationDiscovery_DiscoversE2EPackages(t *testing.T) {
	root := findModuleRoot(t)
	pkgs, err := discoverPackagesUnderTag(root, "e2e")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(pkgs), 1,
		"expected ≥1 e2e package discovered, got %d: %v", len(pkgs), pkgs)
	t.Logf("discovered %d e2e packages", len(pkgs))
}

// TestArchtest_CIIntegrationDiscovery_WorkflowUsesGoList asserts that the
// integration-test job's main step uses `go list -tags=integration` for
// package discovery, not a hardcoded glob list. The forbidden snippet is
// the exact pre-S0 invocation header — its presence indicates a literal
// revert of the discovery change.
func TestArchtest_CIIntegrationDiscovery_WorkflowUsesGoList(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	assert.Contains(t, step.Run, "go list -tags=integration",
		"integration-test step must auto-discover via `go list -tags=integration ...`; "+
			"see CI-INTEGRATION-DISCOVERY-01")

	forbidden := "go test -tags=integration,e2e -covermode=atomic -coverprofile=coverage-integration.out ./adapters/..."
	assert.NotContains(t, step.Run, forbidden,
		"integration-test step must not hardcode package globs after `go test`; "+
			"use $pkgs from `go list` discovery instead")
}

// TestArchtest_CIIntegrationDiscovery_GuardsEmptyDiscovery asserts that the
// step fails fast if discovery returns zero packages. Without this guard, a
// misconfigured build context (e.g., a tag typo) could silently zero out
// the integration job and turn it into a no-op pass.
func TestArchtest_CIIntegrationDiscovery_GuardsEmptyDiscovery(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	// Either `test -n "$pkgs"` or `[ -z "$pkgs" ]` form is acceptable.
	guardRE := regexp.MustCompile(`(test\s+-n\s+["']?\$\{?pkgs\}?["']?|\[\s+-z\s+["']?\$\{?pkgs\}?["']?\s+\])`)
	assert.True(t, guardRE.MatchString(step.Run),
		"integration-test step must fail fast on empty discovery (e.g., `test -n \"$pkgs\" || exit 1`)")
}

// TestArchtest_CIIntegrationDiscovery_WorkflowInvokesGoTestOnDiscoveredPkgs
// asserts the `go test` invocation passes $pkgs (or ${pkgs}) as the package
// argument, not a literal glob. This is the symmetric positive check to
// WorkflowUsesGoList: the discovery output must actually flow into the
// test invocation.
func TestArchtest_CIIntegrationDiscovery_WorkflowInvokesGoTestOnDiscoveredPkgs(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	invokeRE := regexp.MustCompile(`go test\s+[^\n]*\$\{?pkgs\}?`)
	assert.True(t, invokeRE.MatchString(step.Run),
		"integration-test step's `go test` must run on $pkgs from discovery; "+
			"got run block:\n%s", step.Run)
}

// TestArchtest_CIIntegrationDiscovery_FixtureMetaTest verifies the
// discoverPackagesUnderTag walker classifies synthetic files correctly:
//
//   - `//go:build integration`              → discovered under "integration"
//   - `//go:build e2e`                      → discovered under "e2e", NOT "integration"
//   - `//go:build integration && otelcollector` → NOT discovered under "integration"
//     (compound tag; covered by separate CI step)
//   - no //go:build line                    → NOT discovered
//   - `//go:build integration_cluster`      → NOT discovered under "integration"
//     (different tag name; covered by separate vet step)
//   - `//go:build integration || e2e`       → discovered under both tags
//
// Each fixture lives in its own subdirectory so the directory-level
// dedup logic in discoverPackagesUnderTag does not collapse multiple
// fixtures into one entry.
func TestArchtest_CIIntegrationDiscovery_FixtureMetaTest(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name    string
		content string
		wantInt bool
		wantE2E bool
	}{
		{"plain_integration", "//go:build integration\n\npackage f\n", true, false},
		{"plain_e2e", "//go:build e2e\n\npackage f\n", false, true},
		{"compound_otel", "//go:build integration && otelcollector\n\npackage f\n", false, false},
		{"no_tag", "package f\n", false, false},
		{"cluster", "//go:build integration_cluster\n\npackage f\n", false, false},
		{"or_form", "//go:build integration || e2e\n\npackage f\n", true, true},
	}

	root := t.TempDir()
	for _, fx := range fixtures {
		sub := filepath.Join(root, fx.name)
		require.NoError(t, os.MkdirAll(sub, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(sub, "f.go"), []byte(fx.content), 0o644))
	}

	intPkgs, err := discoverPackagesUnderTag(root, "integration")
	require.NoError(t, err)
	e2ePkgs, err := discoverPackagesUnderTag(root, "e2e")
	require.NoError(t, err)

	for _, fx := range fixtures {
		intHit := slices.Contains(intPkgs, fx.name)
		e2eHit := slices.Contains(e2ePkgs, fx.name)
		assert.Equal(t, fx.wantInt, intHit, "integration discovery for fixture %s", fx.name)
		assert.Equal(t, fx.wantE2E, e2eHit, "e2e discovery for fixture %s", fx.name)
	}
}
