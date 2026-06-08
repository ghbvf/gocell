// INVARIANT: SVCTOKEN-CALLER-CELL-REQUIRED-01
package archtest

import "testing"

// TestSVCTOKEN_CALLER_CELL_REQUIRED_01 enforces that every call to
// auth.GenerateServiceToken passes a valid cell-ID string literal as its
// second argument (callerCell). Green now that GenerateServiceToken takes
// callerCell and every flat-tag-visible call site passes it; the build-tagged
// satellite-module blind spot is tracked by the _Wave5_RED companion below (#1590).
func TestSVCTOKEN_CALLER_CELL_REQUIRED_01(t *testing.T) {
	t.Parallel()
	Report(t, ruleSvctokenCallerCellRequired01,
		CheckSvctokenCallerCellRequired01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestSVCTOKEN_CALLER_CELL_REQUIRED_01_BuildTaggedFilesScanned_Wave5_RED is a
// RED-step regression test (TDD per ai-robust.md) for PR445-FU finding F2.
//
// The production rule TestSVCTOKEN_CALLER_CELL_REQUIRED_01 calls
// Run(t, Production(TypedOpts{...})) with FlatNonDefaultTags() — so packages.Load
// uses all known build tags and includes every file gated by `//go:build <tag>`.
// Two real callsites of auth.GenerateServiceToken are gated this way and
// therefore escape the rule without the flat-tag load:
//
//   - examples/ssobff/walkthrough_test.go  (//go:build integration)
//   - tests/integration/internal_rpc_caller_cell_test.go  (//go:build integration)
//   - tests/integration/l2atomicity/helpers_test.go  (//go:build integration)
//
// Wave 5 introduces FlatNonDefaultTags() — the union of every tag tracked
// in KnownNonDefaultTags() — and the production rule loads once with all
// tags simultaneously. This test asserts the load contract directly:
// build-tagged files MUST be loaded so the rule's scan loop actually reaches
// their callsites.
//
// Wave 1 (tags=nil): integration-tagged files NOT in loaded set →
// assertion fails → RED.
//
// Wave 5 (FlatNonDefaultTags single load): integration-tagged files
// loaded → assertion passes → GREEN. The sub-test mirrors the production
// loader exactly.
//
// Single-load (vs the obvious "iterate KnownNonDefaultTags() and call
// Run(t, Typed(...)) per tag-set") avoids retaining 7 independent type graphs in
// SharedResolver's package-cache, which OOM'd CI runners with ~7GB RAM
// before this fix.
func TestSVCTOKEN_CALLER_CELL_REQUIRED_01_BuildTaggedFilesScanned_Wave5_RED(t *testing.T) {
	t.Parallel()

	// Mirror the production rule's loader call (single flat-tag load) exactly.
	loadedFiles := map[string]bool{}
	_ = Run(t, Production(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			for _, file := range p.Files {
				loadedFiles[p.Rel(file)] = true
			}
			return nil
		})

	// Files known to contain auth.GenerateServiceToken callsites under
	// `//go:build integration`. Wave 5's FlatNonDefaultTags load must
	// load each.
	expectedScanned := []string{
		"examples/ssobff/walkthrough_test.go",
		"tests/integration/internal_rpc_caller_cell_test.go",
		"tests/integration/l2atomicity/helpers_test.go",
	}

	var missing []string
	for _, want := range expectedScanned {
		if !loadedFiles[want] {
			missing = append(missing, want)
		}
	}

	if len(missing) > 0 {
		t.Errorf("SVCTOKEN-CALLER-CELL-REQUIRED-01: %d build-tagged files are "+
			"silently skipped by the current tags=nil load — these contain "+
			"auth.GenerateServiceToken callsites that the rule MUST scan: %v",
			len(missing), missing)
	}
}
