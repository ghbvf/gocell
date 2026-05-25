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

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// C3 — DOC-NAME-01 include path traversal rejected.
func TestValidateDOCNAME01_Include_PathTraversalRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	secretName := "outside-secret.md"
	secret := filepath.Join(filepath.Dir(root), secretName)
	require.NoError(t, os.WriteFile(secret, []byte("sso-bff leak\n"), 0o644))
	t.Cleanup(func() { _ = os.Remove(secret) })

	writeFile(t, root, "docs/architecture/naming-guard.yaml",
		"include:\n  - ../"+secretName+"\nreplacements:\n"+
			"  - literal: sso-bff\n    replacement: ssobff\n")

	results := NewValidator(validProject(), root, clock.Real()).validateDOCNAME01()

	for _, r := range results {
		assert.NotContains(t, r.File, "outside-secret",
			"must not read/scan an include target that escapes the project root")
	}
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
	require.NotPanics(t, func() { _ = verify06(v) })
}

// F22 — filterByPhase with no phases returns empty.
func TestFilterByPhase_NoPhases_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	require.Empty(t, filterByPhase(allRules))
}
