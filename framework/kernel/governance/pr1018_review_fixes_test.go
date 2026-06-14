package governance

// Tests for PR #1018 review findings (RED wave). These assert the post-fix
// behavior and fail against the pre-fix code:
//   - C2: CH-04 handler parse failure must fail closed (emit a finding), not
//     silently skip the contract.
//   - C3: DOC-NAME-01 include targets that escape the project root must be
//     rejected; oversize include targets must be skipped (no OOM read).
//   - C5: NewValidator must initialize runCtx so a VERIFY-06 Detect call that
//     bypasses run() does not nil-panic.
//   - F22: filterByPhase with no phases returns empty (boundary).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// C2 — CH-04 fail-closed on unparseable handler.
func TestCheckCH04_HandlerParseFailure_FailsClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const contractID = "http.test.v1"
	sliceRelDir := "cells/testcell/slices/testslice"
	sliceAbsDir := filepath.Join(root, sliceRelDir)
	require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
	// Syntactically invalid Go: go/parser fails → CH-04 must not silently skip.
	require.NoError(t, os.WriteFile(filepath.Join(sliceAbsDir, "handler.go"),
		[]byte("package x\n\nfunc broken( {\n"), 0o644))

	project := makeProject(contractID, sliceRelDir)
	project.Contracts[contractID] = makeContract(
		contractID, "contracts/http/test/v1/contract.yaml",
		map[int]metadata.HTTPResponseMeta{400: {}})

	results := NewValidator(project, root, clock.Real()).checkCH04()

	require.NotEmpty(t, results,
		"unparseable handler must fail closed (CH-04 finding), not silent-skip")
	assert.Equal(t, codeCH04, results[0].Code)
	assert.Equal(t, SeverityError, results[0].Severity)
}

// C2 — CH-05 fail-closed on unparseable handler (symmetric with CH-04).
func TestCheckCH05_HandlerParseFailure_FailsClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const contractID = "http.test.v1"
	sliceRelDir := "cells/testcell/slices/testslice"
	sliceAbsDir := filepath.Join(root, sliceRelDir)
	require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sliceAbsDir, "handler.go"),
		[]byte("package x\n\nfunc broken( {\n"), 0o644))

	project := makeProject(contractID, sliceRelDir)
	c := makeContract(contractID, "contracts/http/test/v1/contract.yaml", nil)
	// CH-05 only inspects contracts with a uuid-format path param.
	c.Endpoints.HTTP.PathParams = map[string]metadata.ParamSchema{"id": {Format: "uuid"}}
	project.Contracts[contractID] = c

	results := NewValidator(project, root, clock.Real()).checkCH05()

	require.NotEmpty(t, results,
		"unparseable handler must fail closed for CH-05 too (symmetric with CH-04)")
	assert.Equal(t, codeCH05, results[0].Code)
	assert.Equal(t, SeverityError, results[0].Severity)
}

// C3 — DOC-NAME-01 include path traversal rejected.
func TestValidateDOCNAME01_Include_PathTraversalRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	secretName := "outside-secret.md"
	secret := filepath.Join(filepath.Dir(root), secretName)
	require.NoError(t, os.WriteFile(secret, []byte("sso-bff leak\n"), 0o644))
	t.Cleanup(func() { _ = os.Remove(secret) })

	// In-root control file with the same literal, so the assertion is not
	// vacuous: the rule must still scan README.md while rejecting the escaping
	// include.
	writeFile(t, root, "README.md", "sso-bff inside root\n")
	writeFile(t, root, "docs/architecture/naming-guard.yaml",
		"include:\n  - README.md\n  - ../"+secretName+"\nreplacements:\n"+
			"  - literal: sso-bff\n    replacement: ssobff\n")

	results := NewValidator(validProject(), root, clock.Real()).validateDOCNAME01()

	var sawReadme, sawSecret bool
	for _, r := range results {
		if r.File == "README.md" {
			sawReadme = true
		}
		if strings.Contains(r.File, "outside-secret") {
			sawSecret = true
		}
	}
	assert.True(t, sawReadme,
		"in-root README.md must still be scanned (proves the rule isn't vacuously skipping)")
	assert.False(t, sawSecret,
		"root-escaping include target must not be read/scanned")
}

// C3 — DOC-NAME-01 include oversize target skipped (no unbounded read).
func TestValidateDOCNAME01_Include_OversizeFileSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// One matching literal on line 1, then ~12MiB of short lines (> size cap).
	content := "sso-bff\n" + strings.Repeat("x\n", 6*1024*1024)
	writeFile(t, root, "BIG.md", content)
	writeFile(t, root, "docs/architecture/naming-guard.yaml",
		"include:\n  - BIG.md\nreplacements:\n"+
			"  - literal: sso-bff\n    replacement: ssobff\n")

	results := NewValidator(validProject(), root, clock.Real()).validateDOCNAME01()

	for _, r := range results {
		assert.NotEqual(t, "BIG.md", r.File,
			"oversize include target must be skipped, not scanned")
	}
}

// C5 — NewValidator initializes runCtx so direct Detect bypass does not panic.
func TestNewValidator_RunCtxDefaultsToBackground(t *testing.T) {
	t.Parallel()
	v := NewValidator(validProject(), "", clock.Real())
	require.NotNil(t, v.runCtx,
		"NewValidator must default runCtx to context.Background() so a Detect "+
			"call that bypasses run() does not nil-panic")
}

func TestVERIFY06_DirectDetect_NoPanicAfterNewValidator(t *testing.T) {
	t.Parallel()
	v := NewValidator(validProject(), "", clock.Real())
	var verify06 func(*Validator) []ValidationResult
	for i := range allRules {
		if allRules[i].Code == codeVERIFY06 {
			verify06 = allRules[i].Detect
			break
		}
	}
	require.NotNil(t, verify06, "VERIFY-06 must be registered in allRules")
	var got []ValidationResult
	require.NotPanics(t, func() { got = verify06(v) })
	assert.Empty(t, got, "VERIFY-06 with empty root must yield no findings")
}

// F22 — filterByPhase with no phases returns empty.
func TestFilterByPhase_NoPhases_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	require.Empty(t, filterByPhase(allRules))
}

// C1 — StrictRuleCodes is exactly the PhaseStrict registry so the derived
// --strict help text can never drift from the rules that run.
func TestStrictRuleCodes_MatchesPhaseStrictRegistry(t *testing.T) {
	t.Parallel()
	got := StrictRuleCodes()
	var want []RuleCode
	for i := range allRules {
		if allRules[i].Phase == PhaseStrict {
			want = append(want, allRules[i].Code)
		}
	}
	require.Equal(t, want, got)
	// Regression guard for the review finding: the strict set must include the
	// rules the old hand-written help text omitted.
	assert.Contains(t, got, codeFMT19)
	assert.Contains(t, got, codeDOCNAME01)
}
