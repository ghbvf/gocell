// INVARIANT: CI-INTEGRATION-DISCOVERY-01: integration-test step uses `go list -tags=integration` discovery, not hardcoded globs
package archtest

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// discoverPackagesUnderTag walks rootDir via the scanner framework and
// returns relative paths of every directory containing at least one .go
// file whose //go:build expression evaluates true under {tag} alone AND
// false under no tags.
//
// "{tag} alone" matches the visibility a `go list -tags=<tag>` invocation
// would produce: a file with `integration && otelcollector` is NOT
// discovered because that expression is false under {integration} alone.
// This mirrors the carve-out semantics of the dedicated OTel/race CI steps.
//
// Uses scanner.ModuleScope so the default skip-dir set (vendor, testdata,
// worktrees, generated, .git, node_modules) is enforced uniformly with
// every other archtest walk per SCANNER-FRAMEWORK-USAGE-01.
func discoverPackagesUnderTag(rootDir, tag string) ([]string, error) {
	files, err := scanner.ModuleScope(rootDir, scanner.IncludeTests()).Files()
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var pkgs []string
	for _, path := range files {
		ok, parseErr := fileHasExclusivelyTag(path, tag)
		if parseErr != nil {
			return nil, parseErr
		}
		if !ok {
			continue
		}
		dir := filepath.Dir(path)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		rel, relErr := filepath.Rel(rootDir, dir)
		if relErr != nil {
			rel = dir
		}
		pkgs = append(pkgs, rel)
	}
	sort.Strings(pkgs)
	return pkgs, nil
}

// fileHasExclusivelyTag returns true iff the file's header //go:build line
// evaluates true under a CI context with {tag} set and false under the same
// context without {tag} — i.e., the file IS gated on the tag and would be
// visible to `go list -tags=<tag>` on a standard Linux CI runner.
//
// The CI context is modeled as the toolchain defaults (GOOS/GOARCH/cgo/go1.x)
// plus the supplied tag. This correctly handles constraints like
// `//go:build integration && unix` which are satisfied on Linux CI with
// -tags=integration (unix is an implicit toolchain default on Linux).
//
// Compound expressions like `integration && otelcollector` evaluate false
// under {integration} + defaults (because otelcollector is not a default),
// so files using such constraints are NOT returned for tag="integration".
// They belong to dedicated CI steps (OTel smoke, integration_cluster vet,
// etc.) and must not be confused with the main integration-test gate.
//
// Legacy plus-build form is honored via typeseval.ParseBuildConstraint.
func fileHasExclusivelyTag(path, tag string) (bool, error) {
	expr, err := typeseval.ParseBuildConstraint(path)
	if err != nil {
		return false, err
	}
	if expr == nil {
		return false, nil
	}
	// "Exclusively gated on <tag>": CI workflow runs in a default Linux context
	// with -tags=<tag>. The file must be discovered iff that tag is set on top
	// of the toolchain defaults.
	withTagCtx := expr.Eval(typeseval.BuildContextPredicate(tag))
	withoutTagCtx := expr.Eval(typeseval.BuildContextPredicate())
	return withTagCtx && !withoutTagCtx, nil
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
// the walker discovers a non-empty set AND every package in a small
// sentinel list. Sentinels are foundational integration coverage anchors
// (PG adapter, integration-test root, shared testcontainer helpers) whose
// disappearance signals either walker breakage or major refactor — the
// latter requiring an explicit update of this list rather than a silent
// numeric-threshold drift. Avoiding a free-floating count threshold (which
// ages with the codebase) keeps the gate stable across legitimate package
// reorgs that don't affect integration coverage.
func TestArchtest_CIIntegrationDiscovery_DiscoversIntegrationPackages(t *testing.T) {
	root := findModuleRoot(t)
	pkgs, err := discoverPackagesUnderTag(root, "integration")
	require.NoError(t, err)
	require.NotEmpty(t, pkgs,
		"no integration packages discovered — walker likely broken")

	sentinels := []string{
		"adapters/postgres",
		"tests/integration",
		"tests/testutil",
	}
	for _, s := range sentinels {
		assert.Contains(t, pkgs, s,
			"sentinel integration package %q must be discovered; "+
				"walker broken or package relocated (update sentinels intentionally)", s)
	}
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
// package discovery and contains no deeper-than-root package globs (e.g.,
// `./adapters/...`, `./cells/configcore/...`). The whole-module probe
// `./...` (used inside the `go list` call itself) is allowed — it's the
// deeper-segment globs that signal hardcoded-list regression.
func TestArchtest_CIIntegrationDiscovery_WorkflowUsesGoList(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	assert.Contains(t, step.Run, "go list -tags=integration",
		"integration-test step must auto-discover via `go list -tags=integration ...`; "+
			"see CI-INTEGRATION-DISCOVERY-01")

	hardcodedGlobRE := regexp.MustCompile(`\./[a-z][a-zA-Z0-9_-]*/\.\.\.`)
	matches := hardcodedGlobRE.FindAllString(step.Run, -1)
	assert.Empty(t, matches,
		"integration-test step must not hardcode package globs (found %v); "+
			"use the discovered package set from `go list` instead", matches)
}

// TestArchtest_CIIntegrationDiscovery_GuardsEmptyDiscovery asserts that the
// step fails fast if discovery returns zero packages. The guard must combine
// (a) a discovery-emptiness check AND (b) an explicit `exit 1` on the empty
// branch — a bare check without exit path is decorative (`test ... && echo
// ok` would parse fine but never fail).
//
// Accepted forms (string scalar OR bash array; positive or negated):
//   - test -n "$pkgs" || { ...; exit 1; }     (current: bash array form)
//   - [ -z "$pkgs" ] && { ...; exit 1; }
//   - test "${#pkgs[@]}" -gt 0 || { ...; exit 1; }
//   - [ "${#pkgs[@]}" -eq 0 ] && { ...; exit 1; }
func TestArchtest_CIIntegrationDiscovery_GuardsEmptyDiscovery(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	guardRE := regexp.MustCompile(
		`(test\s+-n\s+["']?\$\{?pkgs\}?["']?\s*\|\|.*\bexit\s+1\b` +
			`|\[\s+-z\s+["']?\$\{?pkgs\}?["']?\s+\]\s*&&.*\bexit\s+1\b` +
			`|test\s+"\$\{#pkgs\[@\]\}"\s+-gt\s+0\s*\|\|.*\bexit\s+1\b` +
			`|\[\s+"\$\{#pkgs\[@\]\}"\s+-eq\s+0\s+\]\s*&&.*\bexit\s+1\b)`,
	)
	assert.True(t, guardRE.MatchString(step.Run),
		"integration-test step must fail fast on empty discovery — the empty-check "+
			"must be paired with `|| { ...; exit 1; }` (positive form) or `&& { ...; exit 1; }` "+
			"(negated form); a check without an exit path is decorative")
}

// TestArchtest_CIIntegrationDiscovery_WorkflowInvokesGoTestOnDiscoveredPkgs
// asserts the `go test` invocation passes the discovered package set as
// arguments — either `$pkgs` (string scalar) or `"${pkgs[@]}"` (bash array,
// current form). This is the symmetric positive check to WorkflowUsesGoList:
// discovery output must actually flow into the test invocation.
func TestArchtest_CIIntegrationDiscovery_WorkflowInvokesGoTestOnDiscoveredPkgs(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	invokeRE := regexp.MustCompile(`go test\s+[^\n]*(\$\{?pkgs\}?|"\$\{pkgs\[@\]\}")`)
	assert.True(t, invokeRE.MatchString(step.Run),
		"integration-test step's `go test` must run on the discovered package set "+
			"($pkgs or \"${pkgs[@]}\"); got run block:\n%s", step.Run)
}

// TestArchtest_CIIntegrationDiscovery_WorkflowInvokesExactlyOneGoTest asserts
// the integration-test step contains exactly one `go test` invocation. A
// second hardcoded `go test ./somepath/...` co-existing alongside the
// discovered set would silently dilute the discovery guarantee — the
// uniqueness constraint forces the discovery output to be the single source
// of truth for what gets tested.
//
// The line-anchored regex `(?m)^\s*go test\s` avoids false positives from
// `go test` mentioned in shell comments or echo strings.
func TestArchtest_CIIntegrationDiscovery_WorkflowInvokesExactlyOneGoTest(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	goTestRE := regexp.MustCompile(`(?m)^\s*go test\s`)
	matches := goTestRE.FindAllString(step.Run, -1)
	assert.Len(t, matches, 1,
		"integration-test step must invoke `go test` exactly once on the discovered "+
			"package set; found %d invocations: %v", len(matches), matches)
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
//   - pre-Go-1.17 `// +build integration` (no `//go:build` line) → discovered;
//     typeseval.ParseBuildConstraint honors the legacy directive form
//   - production .go (filename != _test.go) with the tag → discovered;
//     mirrors the workflow set-diff's .GoFiles coverage
//   - `_test.go` filename with the tag → discovered; mirrors the workflow
//     set-diff's .TestGoFiles / .XTestGoFiles coverage
//
// Each fixture lives in its own subdirectory so the directory-level
// dedup logic in discoverPackagesUnderTag does not collapse multiple
// fixtures into one entry. The `filename` field defaults to "f.go" when
// blank; explicit values document filename-specific intent.
func TestArchtest_CIIntegrationDiscovery_FixtureMetaTest(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name     string
		filename string // defaults to "f.go" when empty
		content  string
		wantInt  bool
		wantE2E  bool
	}{
		{name: "plain_integration", content: "//go:build integration\n\npackage f\n", wantInt: true},
		{name: "plain_e2e", content: "//go:build e2e\n\npackage f\n", wantE2E: true},
		{name: "compound_otel", content: "//go:build integration && otelcollector\n\npackage f\n"},
		{name: "no_tag", content: "package f\n"},
		{name: "cluster", content: "//go:build integration_cluster\n\npackage f\n"},
		{name: "or_form", content: "//go:build integration || e2e\n\npackage f\n", wantInt: true, wantE2E: true},
		// Legacy plus-build form is honored via typeseval.ParseBuildConstraint.
		{name: "old_plus_build", content: "// +build integration\n\npackage f\n", wantInt: true},
		// integration && unix: unix is an implicit toolchain default on Linux CI runners,
		// so this file IS discovered under -tags=integration on a Linux CI host.
		// This locks in the behavior change from the BuildContextPredicate migration:
		// the old fileHasExclusivelyTag used {tag} alone and would have missed this file.
		{name: "integration_with_unix", content: "//go:build integration && unix\n\npackage f\n", wantInt: true},
		// Filename-typed cases: explicit production .go vs _test.go to mirror
		// workflow set-diff symmetry across .GoFiles / .TestGoFiles axes.
		{
			name: "production_only_integration", filename: "production.go",
			content: "//go:build integration\n\npackage f\n", wantInt: true,
		},
		{
			name: "test_file_integration", filename: "service_integration_test.go",
			content: "//go:build integration\n\npackage f\n", wantInt: true,
		},
	}

	root := t.TempDir()
	for _, fx := range fixtures {
		sub := filepath.Join(root, fx.name)
		require.NoError(t, os.MkdirAll(sub, 0o755))
		fname := fx.filename
		if fname == "" {
			fname = "f.go"
		}
		require.NoError(t, os.WriteFile(filepath.Join(sub, fname), []byte(fx.content), 0o644))
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
