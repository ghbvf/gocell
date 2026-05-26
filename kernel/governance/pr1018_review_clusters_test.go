package governance

// RED tests for the PR #1018 cluster review (F1/F2/F8/F9). These assert the
// post-fix behavior and fail against the pre-fix code:
//   - C1/F1: generated-path assembly from a contract ID must reject unsafe path
//     segments (empty / "." / ".." / containing a path separator) and stay
//     within the project root — a malformed contract ID must not resolve a
//     handler file outside its own generated package.
//   - C1/F8: CH-04 parse-failure findings must not leak the absolute CI-worker
//     path into the user-visible Message.
//   - C2/F2: run() must surface ctx cancellation that occurs during the final
//     rule's Detect; the per-contract CH loops must honor a canceled runCtx.
//   - C5/F9: a handler that writes a response with a non-static errcode.Kind
//     (CH-04 cannot statically resolve the status) must fail closed (emit a
//     finding), not silently skip the contract.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// C1/F1 — the shared generated-path guard rejects unsafe segments and keeps the
// assembled path within root.
func TestSafeJoinUnderRoot_RejectsUnsafeSegments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	unsafe := []string{"", ".", "..", "a/b", "a" + string(os.PathSeparator) + "b"}
	for _, seg := range unsafe {
		_, ok := safeJoinUnderRoot(root, "generated", "contracts", seg)
		assert.Falsef(t, ok, "segment %q must be rejected by the generated-path guard", seg)
	}

	got, ok := safeJoinUnderRoot(root, "generated", "contracts", "http", "order", "v1")
	require.True(t, ok, "well-formed segments must be accepted")
	assert.True(t, strings.HasPrefix(got, root),
		"assembled path must stay within root")
}

// C1/F1 — findHandlerFile must not resolve a codegen handler when the contract
// ID contains a path separator in a segment (contract IDs are dot-separated and
// never contain "/"). A planted in-root file at the would-be location proves
// the assertion is not vacuous.
func TestFindHandlerFile_RejectsSeparatorSegment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Control: a well-formed codegen contract resolves its handler_gen.go.
	const goodID = "http.order.v1"
	goodDir := filepath.Join(root, "generated", "contracts", "http", "order", "v1")
	require.NoError(t, os.MkdirAll(goodDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(goodDir, "handler_gen.go"), []byte("package x\n"), 0o644))

	// Malicious: a "/" inside a segment. Plant the file at the path the pre-fix
	// join would reach so the test fails RED (returns the planted path) until
	// the guard rejects the separator-bearing segment.
	const badID = "http.a/b.v1"
	badDir := filepath.Join(root, "generated", "contracts", "http", "a", "b", "v1")
	require.NoError(t, os.MkdirAll(badDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "handler_gen.go"), []byte("package x\n"), 0o644))

	project := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			goodID: {ID: goodID, Kind: "http", Codegen: true},
			badID:  {ID: badID, Kind: "http", Codegen: true},
		},
	}

	require.NotEmpty(t, findHandlerFile(project, goodID, root),
		"well-formed codegen contract must resolve its handler_gen.go")
	assert.Empty(t, findHandlerFile(project, badID, root),
		"contract ID with a path-separator segment must not resolve a handler file")
}

// C2/F2 — cancellation during the final rule's Detect must surface as an error.
// Pre-fix run() only checks ctx at the top of each loop iteration, so a cancel
// in the last rule is lost (no next iteration to observe it).
func TestRun_CancelDuringLastRule_ReturnsErr(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	v := NewValidator(validProject(), "", clock.Real())
	rules := []Rule{
		{Code: codeREF01, Phase: PhaseBase, Detect: func(*Validator) []ValidationResult {
			cancel()
			return nil
		}},
	}
	_, err := v.run(ctx, rules, false)
	require.ErrorIs(t, err, context.Canceled,
		"cancellation during the final rule's Detect must be surfaced as an error")
}

// C2/F2 — the per-contract CH-04 loop must short-circuit on a canceled runCtx.
func TestCheckCH04_CanceledRunCtx_ShortCircuits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const contractID = "http.test.v1"
	sliceRelDir := "cells/testcell/slices/testslice"
	sliceAbsDir := filepath.Join(root, sliceRelDir)
	require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
	// A handler with no auth.Mount correlation → CH-04 emits a finding when run.
	require.NoError(t, os.WriteFile(filepath.Join(sliceAbsDir, "handler.go"),
		[]byte("package x\n\nfunc handleSomething() {}\n"), 0o644))

	project := makeProject(contractID, sliceRelDir)
	project.Contracts[contractID] = makeContract(contractID,
		"contracts/http/test/v1/contract.yaml",
		map[int]metadata.HTTPResponseMeta{400: {}})

	v := NewValidator(project, root, clock.Real())

	// Sanity: without cancellation the rule produces a finding (non-vacuous).
	require.NotEmpty(t, v.checkCH04(), "control: CH-04 must produce a finding when not canceled")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v.runCtx = ctx
	assert.Empty(t, v.checkCH04(),
		"a canceled runCtx must short-circuit the per-contract CH-04 loop")
}

// C1/F8 — CH-04 parse-failure Message must not embed the absolute root path.
func TestCheckCH04_ParseFailureMessage_NoAbsolutePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const contractID = "http.test.v1"
	sliceRelDir := "cells/testcell/slices/testslice"
	sliceAbsDir := filepath.Join(root, sliceRelDir)
	require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sliceAbsDir, "handler.go"),
		[]byte("package x\n\nfunc broken( {\n"), 0o644))

	project := makeProject(contractID, sliceRelDir)
	project.Contracts[contractID] = makeContract(contractID,
		"contracts/http/test/v1/contract.yaml",
		map[int]metadata.HTTPResponseMeta{400: {}})

	results := NewValidator(project, root, clock.Real()).checkCH04()
	require.NotEmpty(t, results)
	assert.NotContains(t, results[0].Message, root,
		"parse-failure message must not leak the absolute CI-worker path")
}

// C5/F9 — a handler writing a response via errcode.New with a non-static Kind
// must fail closed (CH-04 finding) rather than silently skipping the contract.
func TestCheckCH04_DynamicErrcodeKind_FailsClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const contractID = "http.test.v1"
	sliceRelDir := "cells/testcell/slices/testslice"
	sliceAbsDir := filepath.Join(root, sliceRelDir)
	require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))

	// dynKind is a variable, not a static errcode.KindXxx selector — CH-04
	// cannot resolve the resulting status statically.
	writeHandlerFile(t, sliceAbsDir, contractID, `
var dynKind = errcode.KindInvalid

func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.New(dynKind, errcode.ErrValidationFailed, "x")
}
`)

	project := makeProject(contractID, sliceRelDir)
	// Contract declares no error statuses — pre-fix CH-04 silently passes.
	project.Contracts[contractID] = makeContract(contractID,
		"contracts/http/test/v1/contract.yaml", nil)

	results := NewValidator(project, root, clock.Real()).checkCH04()

	require.NotEmpty(t, results,
		"a non-static errcode.Kind must fail closed (CH-04 finding), not silent-skip")
	assert.Equal(t, codeCH04, results[0].Code)
	assert.Equal(t, SeverityError, results[0].Severity)
}
