//go:build archtest

// INVARIANT: CI-INTEGRATION-DISCOVERY-01: integration-test discovers via go list over the go.work module funnel, not hardcoded globs
package archtest

import (
	"bytes"
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
	files, err := ModuleScope(rootDir, IncludeTests()).Files()
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
	expr, err := ParseBuildConstraint(path)
	if err != nil {
		return false, err
	}
	if expr == nil {
		return false, nil
	}
	// "Exclusively gated on <tag>": CI workflow runs in a default Linux context
	// with -tags=<tag>. The file must be discovered iff that tag is set on top
	// of the toolchain defaults.
	withTagCtx := expr.Eval(BuildContextPredicate(tag))
	withoutTagCtx := expr.Eval(BuildContextPredicate())
	return withTagCtx && !withoutTagCtx, nil
}

// readWorkflowStep reads <workflowFile> under .github/workflows/ and returns
// the named step from the named job. Step + job + workflow names are coupled
// to the YAML by string; renaming any of them is a coordinated change with
// this archtest.
func readWorkflowStep(t *testing.T, workflowFile, jobName, stepNamePrefix string) workflowStep {
	t.Helper()
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, ".github", "workflows", workflowFile)))
	require.NoError(t, err)

	var cfg workflowConfig
	require.NoError(t, yaml.NewDecoder(bytes.NewReader(body)).Decode(&cfg))

	job, ok := cfg.Jobs[jobName]
	require.True(t, ok, "job %q missing from %s", jobName, workflowFile)

	for _, step := range job.Steps {
		if strings.HasPrefix(step.Name, stepNamePrefix) {
			return step
		}
	}
	require.FailNowf(t, "step missing", "step with name prefix %q missing from %s::%s", stepNamePrefix, workflowFile, jobName)
	return workflowStep{}
}

// readIntegrationTestStep reads the integration-test step of _build-lint.yml.
// Thin wrapper kept for clarity at the call site.
func readIntegrationTestStep(t *testing.T) workflowStep {
	return readWorkflowStep(t, "_build-lint.yml", "integration-test", "Integration tests (testcontainers)")
}

// readRaceIntegrationStep reads the go-test-race-integration step of
// test-race.yml. The step name contains a long descriptive suffix listing
// the package sets it covers; prefix match keeps this archtest stable when
// that suffix evolves.
func readRaceIntegrationStep(t *testing.T) workflowStep {
	return readWorkflowStep(t, "test-race.yml", "race-pg-integration", "go test -race -tags=integration")
}

// CI-INTEGRATION-DISCOVERY-01 scope note:
//
// This archtest only covers _build-lint.yml::integration-test — the lane
// whose purpose is broad integration coverage. The race-pg-integration step
// of test-race.yml is intentionally a curated narrow list (race detector
// adds ~3-5x runtime and yields false positives on long e2e walkthroughs;
// it only carries unique signal on packages with concurrent goroutine
// boundaries). Forcing race onto every discovered integration package is
// a wrong fit for the race detector's value model.
//
// Race-lane drift protection is handled by a separate archtest
// (CI-RACE-LANE-SUBSET-01) that asserts every package listed in
// race-pg-integration is also discovered by `go list -tags=integration`,
// preventing typos / non-integration paths in the curated list without
// expanding the lane's scope.

// TestArchtest_CIIntegrationDiscovery_DiscoversIntegrationPackages asserts
// the walker discovers a non-empty set AND every package in a small
// sentinel list. Sentinels are foundational integration coverage anchors
// (PG adapter, integration-test root, shared testcontainer helpers, shared
// PG container template) whose disappearance signals either walker breakage
// or major refactor — the
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
		"tests/testutil/pgshare",
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
// `./adapters/...`, `./corecells/configcore/...`). The whole-module probe
// `./...` (used inside the `go list` call itself) is allowed — it's the
// deeper-segment globs that signal hardcoded-list regression.
//
// The optional `go -C <module> list ...` form is accepted: since examples/* are
// their own go.work modules (#1556), discovery iterates each member with `go -C
// "$moddir" list -tags=integration ./...`. The module-funnel coverage is asserted
// separately by TestArchtest_CIIntegrationDiscovery_IteratesModuleFunnel.
func TestArchtest_CIIntegrationDiscovery_WorkflowUsesGoList(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	goListRE := regexp.MustCompile(`go\s+(-C\s+\S+\s+)?list\s+-tags=integration`)
	assert.Regexp(t, goListRE, step.Run,
		"integration-test step must auto-discover via `go list -tags=integration ...` "+
			"(optionally `go -C <module> list ...` for the per-module go.work funnel); "+
			"see CI-INTEGRATION-DISCOVERY-01")

	hardcodedGlobRE := regexp.MustCompile(`\./[a-z][a-zA-Z0-9_-]*/\.\.\.`)
	matches := hardcodedGlobRE.FindAllString(step.Run, -1)
	assert.Empty(t, matches,
		"integration-test step must not hardcode package globs (found %v); "+
			"use the discovered package set from `go list` instead", matches)
}

// TestArchtest_CIIntegrationDiscovery_IteratesModuleFunnel is the upstream half
// of the coverage funnel (#1556). The downstream walker discoverPackagesUnderTag
// finds integration packages by filesystem scan INCLUDING every examples/* module
// — but that only proves coverage if the CI integration lane actually iterates
// every module. A root `go list ./...` stops at the examples/* nested-module
// boundary and would silently drop their integration tests while this archtest
// stayed green (form-only). So assert the step enumerates members through the
// go.work funnel (hack/lib/modules.sh → `gocell::modules::dirs`, derived from
// `go work edit -json`): a module added to go.work is covered with zero hardcoded
// list, and dropping the funnel is a red archtest rather than silent coverage loss.
//
// AI-robust rating: Medium (YAML/bash step content has no Go type system; regex
// form-match — requiring source/invocation on a non-comment line — is the ceiling,
// tracked alongside the existing CI-INTEGRATION-DISCOVERY-01 string-anchor design).
func TestArchtest_CIIntegrationDiscovery_IteratesModuleFunnel(t *testing.T) {
	step := readIntegrationTestStep(t)
	require.NotEmpty(t, step.Run, "integration-test main step run block missing")

	// Require hack/lib/modules.sh to be sourced on a non-comment line (i.e.
	// `source` or `.` — not merely mentioned in a comment).
	sourcedRE := regexp.MustCompile(`(?m)^[^#\n]*\b(source|\.)\s+\S*hack/lib/modules\.sh`)
	assert.Regexp(t, sourcedRE, step.Run,
		"integration-test step must source hack/lib/modules.sh on a non-comment line "+
			"(e.g. `source hack/lib/modules.sh`) so every workspace member — including each "+
			"examples/* go.work module — is enumerated for discovery; a root "+
			"`go list ./...` stops at the nested-module boundary and silently "+
			"drops example integration tests (#1556). See CI-INTEGRATION-DISCOVERY-01.")

	// Require gocell::modules::dirs to be invoked on a non-comment line.
	invokedRE := regexp.MustCompile(`(?m)^[^#\n]*gocell::modules::dirs`)
	assert.Regexp(t, invokedRE, step.Run,
		"integration-test step must invoke `gocell::modules::dirs` on a non-comment line "+
			"(the go.work `use`-derived member list) so per-module discovery covers every module")
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

// TestArchtest_CIRaceLaneSubset_01 — INVARIANT: CI-RACE-LANE-SUBSET-01
//
// The race-pg-integration step of test-race.yml uses a curated hardcoded
// package list (not discovery — see scope note above). To prevent drift
// to non-integration packages or typos, every package listed here must
// also appear in the integration discovery set (`go list -tags=integration`).
//
// Detection: parse the run block, extract every `./<path>/...` glob token,
// then verify each glob's root segment matches at least one package in the
// integration discovery set under that prefix. A glob `./foo/...` matches
// any discovered package whose path starts with `foo/`.
//
// This archtest deliberately does NOT require race-pg-integration to use
// `go list` discovery (that would conflict with the curated narrowness
// the race lane needs). It only guards against drift inside the curated
// list — adding a non-integration package or mistyped path here fails CI.
func TestArchtest_CIRaceLaneSubset_01(t *testing.T) {
	step := readRaceIntegrationStep(t)
	require.NotEmpty(t, step.Run, "race-pg-integration step run block missing")

	globs := raceLanePackageGlobs(step.Run)
	require.NotEmpty(t, globs,
		"race-pg-integration step must list at least one `./<path>/...` package glob; got run block:\n%s", step.Run)

	root := findModuleRoot(t)
	intPkgs, err := discoverPackagesUnderTag(root, "integration")
	require.NoError(t, err)

	for _, glob := range globs {
		matched := false
		for _, pkg := range intPkgs {
			if pkg == glob || strings.HasPrefix(pkg, glob+"/") {
				matched = true
				break
			}
		}
		assert.True(t, matched,
			"race-pg-integration package glob `./%s/...` matches no discovered integration package; "+
				"either the path is typoed, the target package has no //go:build integration files, "+
				"or a non-integration package was added to the race lane. "+
				"See CI-RACE-LANE-SUBSET-01.", glob)
	}
}

// raceLanePackageGlobs extracts the repo-root-relative package globs from a
// race-lane run block, applying any `go -C <module>` working-directory prefix.
// Nested go.work modules (corecells/examples, #1556/#1560) are tested via
// `go -C <module> test ... ./<glob>/...` where the glob is module-relative, so
// the effective repo-relative path is `<module>/<glob>` — matched against
// [discoverPackagesUnderTag]'s repo-relative discovery set.
func raceLanePackageGlobs(run string) []string {
	pathGlobRE := regexp.MustCompile(`\./([a-z][a-zA-Z0-9_/-]*)/\.\.\.`)
	// A `go [ -C <module> ]` invocation starts a command and sets the working-dir
	// prefix; it carries across shell line-continuations to the glob arguments on
	// following lines until the next `go ...` command resets it.
	goCmdRE := regexp.MustCompile(`(^|\s)go\s+(?:-C\s+([a-zA-Z0-9_./-]+)\s+)?`)
	var out []string
	prefix := ""
	for _, line := range strings.Split(run, "\n") {
		line = strings.SplitN(line, "#", 2)[0] // drop shell comments (e.g. an example `./x/...` in a note)
		if cm := goCmdRE.FindStringSubmatch(line); cm != nil {
			if cm[2] != "" {
				prefix = strings.TrimSuffix(filepath.ToSlash(cm[2]), "/") + "/"
			} else {
				prefix = ""
			}
		}
		for _, m := range pathGlobRE.FindAllStringSubmatch(line, -1) {
			out = append(out, prefix+m[1])
		}
	}
	return out
}
