package archtest

// goose_session_locker_fixtures_test.go runs scanGooseSessionLocker against
// the fixture module under testdata/goose_session_locker_fixtures/ to verify:
//
//  1. F1 — Aliased imports ("import g \"…/goose\"") and dot imports
//     ("import . \"…/goose\"") that omit WithSessionLocker are detected.
//     The pre-typed scanner missed both forms.
//  2. F2 — The allowlist matches on repo-relative path (not basename), so a
//     nested file with the same basename as an allowlisted file is NOT
//     exempted.
//  3. The allowlisted top-level file is correctly exempt.
//  4. Compliant call sites (default + alias) yield zero violations.
//
// The fixture lives in its own go.mod so packages.Load can type-check it
// independently of the main module's goose dependency.

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

const fixtureGooseImportPath = "fixturetest/goose_session_locker/internal/goose"

func TestGooseSessionLocker01_Fixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "goose_session_locker_fixtures")

	pkgs, errs, err := typeseval.LoadPackages(fixtureDir, false, nil, "./...")
	require.NoError(t, err, "packages.Load fixture")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	allowlist := map[string]string{
		"adapters/postgres/schema_guard.go": "fixture mirror of production read-only allowlist entry",
	}

	violations, allowlistHits := scanGooseSessionLocker(
		pkgs, fixtureDir, fixtureGooseImportPath, allowlist,
	)

	gotViolations := make([]string, 0, len(violations))
	for _, v := range violations {
		gotViolations = append(gotViolations, v.rel)
	}
	sort.Strings(gotViolations)

	wantViolations := []string{
		"adapters/postgres/aliased_violates.go",
		"adapters/postgres/dot_violates.go",
		"adapters/postgres/nested/schema_guard.go",
	}
	assert.Equal(t, wantViolations, gotViolations,
		"fixture must surface alias-import, dot-import, and nested-same-basename "+
			"violations while exempting the top-level allowlisted schema_guard.go "+
			"and accepting compliant default/alias call sites")

	assert.Equal(t,
		map[string]string{
			"adapters/postgres/schema_guard.go": "fixture mirror of production read-only allowlist entry",
		},
		allowlistHits,
		"allowlist must be keyed on repo-relative path; the top-level "+
			"schema_guard.go is exempt and the nested file with the same basename "+
			"is NOT exempt")
}
