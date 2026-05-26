package governance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAdvisoryHints_Golden locks the canonical text of every advHint* const in
// advisory_hints.go (problem + Fix). These are product value (greppable
// diagnostic strings); silent rewording would change CLI output operators have
// wired into runbooks/grep filters. The const set is derived from the source
// AST (advHintConstNames), so coverage is proven complete rather than asserted
// by a hand-maintained count.
//
// Update procedure when intentionally changing a hint:
//  1. Run `go test ./kernel/governance/... -update` (manually update golden).
//  2. Review the diff in testdata/advisory_hints.golden.txt as a code change.
//  3. Document the rationale in the PR description.
func TestAdvisoryHints_Golden(t *testing.T) {
	hints := map[string]string{
		// ADV-05
		"advHintADV05EmptySubscribers":    advHintADV05EmptySubscribers,
		"advHintADV05EmptySubscribersFix": advHintADV05EmptySubscribersFix,
		// CH-04
		"advHintCH04CorrelationFailed":      advHintCH04CorrelationFailed,
		"advHintCH04CorrelationFailedFix":   advHintCH04CorrelationFailedFix,
		"advHintCH04ParseFailed":            advHintCH04ParseFailed,
		"advHintCH04ParseFailedFix":         advHintCH04ParseFailedFix,
		"advHintCH04DynamicWrite":           advHintCH04DynamicWrite,
		"advHintCH04DynamicWriteFix":        advHintCH04DynamicWriteFix,
		"advHintCH04DynamicKind":            advHintCH04DynamicKind,
		"advHintCH04UnknownHelper":          advHintCH04UnknownHelper,
		"advHintCH04WritePublicNoKind":      advHintCH04WritePublicNoKind,
		"advHintCH04WritePublicDynamicKind": advHintCH04WritePublicDynamicKind,
		// CH-05
		"advHintCH05CorrelationFailed":    advHintCH05CorrelationFailed,
		"advHintCH05CorrelationFailedFix": advHintCH05CorrelationFailedFix,
		"advHintCH05ParseFailed":          advHintCH05ParseFailed,
		"advHintCH05ParseFailedFix":       advHintCH05ParseFailedFix,
		"advHintCH05MissingParseCall":     advHintCH05MissingParseCall,
		"advHintCH05MissingParseCallFix":  advHintCH05MissingParseCallFix,
		// FMT-13
		"advHintFMT13MissingHTTP":         advHintFMT13MissingHTTP,
		"advHintFMT13MissingHTTPFix":      advHintFMT13MissingHTTPFix,
		"advHintFMT13MissingPathParam":    advHintFMT13MissingPathParam,
		"advHintFMT13MissingPathParamFix": advHintFMT13MissingPathParamFix,
		// CCE-01 shared prefix
		"advHintCCE01TriggerPrefix": advHintCCE01TriggerPrefix,
		// CCE-01 per-violation
		"advHintCCE01TriggerNotEvent":         advHintCCE01TriggerNotEvent,
		"advHintCCE01TriggerNotEventFix":      advHintCCE01TriggerNotEventFix,
		"advHintCCE01OwnerMismatch":           advHintCCE01OwnerMismatch,
		"advHintCCE01OwnerMismatchFix":        advHintCCE01OwnerMismatchFix,
		"advHintCCE01SliceNotPublish":         advHintCCE01SliceNotPublish,
		"advHintCCE01SliceNotPublishFix":      advHintCCE01SliceNotPublishFix,
		"advHintCCE01TriggerNotEmitted":       advHintCCE01TriggerNotEmitted,
		"advHintCCE01TriggerNotEmittedFix":    advHintCCE01TriggerNotEmittedFix,
		"advHintCCE01ReverseEmit":             advHintCCE01ReverseEmit,
		"advHintCCE01ReverseEmitFix":          advHintCCE01ReverseEmitFix,
		"advHintCCE01DynamicTopicHelper":      advHintCCE01DynamicTopicHelper,
		"advHintCCE01DynamicTopicHelperFix":   advHintCCE01DynamicTopicHelperFix,
		"advHintCCE01DynamicTopicEmit":        advHintCCE01DynamicTopicEmit,
		"advHintCCE01DynamicTopicEmitFix":     advHintCCE01DynamicTopicEmitFix,
		"advHintCCE01DynamicTopicReceiver":    advHintCCE01DynamicTopicReceiver,
		"advHintCCE01DynamicTopicReceiverFix": advHintCCE01DynamicTopicReceiverFix,
		// DOC-NAME-01
		"advHintDOCNAME01GuardRequired":          advHintDOCNAME01GuardRequired,
		"advHintDOCNAME01GuardRequiredFix":       advHintDOCNAME01GuardRequiredFix,
		"advHintDOCNAME01CannotReadGuard":        advHintDOCNAME01CannotReadGuard,
		"advHintDOCNAME01CannotReadGuardFix":     advHintDOCNAME01CannotReadGuardFix,
		"advHintDOCNAME01CannotParseGuard":       advHintDOCNAME01CannotParseGuard,
		"advHintDOCNAME01CannotParseGuardFix":    advHintDOCNAME01CannotParseGuardFix,
		"advHintDOCNAME01MissingInclude":         advHintDOCNAME01MissingInclude,
		"advHintDOCNAME01MissingIncludeFix":      advHintDOCNAME01MissingIncludeFix,
		"advHintDOCNAME01MissingReplacements":    advHintDOCNAME01MissingReplacements,
		"advHintDOCNAME01MissingReplacementsFix": advHintDOCNAME01MissingReplacementsFix,
		"advHintDOCNAME01InvalidReplacement":     advHintDOCNAME01InvalidReplacement,
		"advHintDOCNAME01InvalidReplacementFix":  advHintDOCNAME01InvalidReplacementFix,
		"advHintDOCNAME01CannotWalk":             advHintDOCNAME01CannotWalk,
		"advHintDOCNAME01CannotWalkFix":          advHintDOCNAME01CannotWalkFix,
		"advHintDOCNAME01InvalidPattern":         advHintDOCNAME01InvalidPattern,
		"advHintDOCNAME01InvalidPatternFix":      advHintDOCNAME01InvalidPatternFix,
		"advHintDOCNAME01CannotReadDoc":          advHintDOCNAME01CannotReadDoc,
		"advHintDOCNAME01CannotReadDocFix":       advHintDOCNAME01CannotReadDocFix,
		"advHintDOCNAME01LegacyLiteral":          advHintDOCNAME01LegacyLiteral,
		"advHintDOCNAME01LegacyLiteralFix":       advHintDOCNAME01LegacyLiteralFix,
		"advHintDOCNAME01CannotScan":             advHintDOCNAME01CannotScan,
		"advHintDOCNAME01CannotScanFix":          advHintDOCNAME01CannotScanFix,
	}
	// Completeness gate (replaces a hand-maintained count): the hints map must
	// cover EVERY advHint* const declared in advisory_hints.go, and contain no
	// key without a backing const. Derived from the source AST so adding a new
	// const without adding it here — or vice versa — fails immediately, instead
	// of silently slipping past a magic-number length check.
	declared := advHintConstNames(t)
	for name := range declared {
		if _, ok := hints[name]; !ok {
			t.Errorf("advHint const %q is declared in advisory_hints.go but missing from this "+
				"test's hints map — add it so its text is golden-locked", name)
		}
	}
	for k := range hints {
		if _, ok := declared[k]; !ok {
			t.Errorf("hints map key %q has no matching advHint* const in advisory_hints.go "+
				"(stale entry?)", k)
		}
	}

	keys := make([]string, 0, len(hints))
	for k := range hints {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString("\n=====\n")
		sb.WriteString(hints[k])
		sb.WriteString("\n-----\n\n")
	}
	got := sb.String()

	goldenPath := filepath.Join("testdata", "advisory_hints.golden.txt")
	want, err := os.ReadFile(filepath.Clean(goldenPath))
	require.NoError(t, err, "golden missing — create with the current hint contents at %s", goldenPath)

	// Normalize CRLF -> LF so Windows checkouts (with autocrlf=true) compare
	// against the LF-encoded golden the same way Linux/macOS do.
	wantNorm := strings.ReplaceAll(string(want), "\r\n", "\n")
	gotNorm := strings.ReplaceAll(got, "\r\n", "\n")

	if gotNorm != wantNorm {
		t.Errorf("advisory hints drift detected — diff against golden %s\n--- want ---\n%s\n--- got ---\n%s",
			goldenPath, wantNorm, gotNorm)
	}
}

// advHintConstNames parses advisory_hints.go and returns the set of every
// package-level const whose name begins with "advHint". This is the single
// source of truth for TestAdvisoryHints_Golden's completeness gate: the test's
// hints map must equal this set, so no advHint const can be added (or removed)
// without the golden test noticing.
func advHintConstNames(t *testing.T) map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "advisory_hints.go", nil, 0)
	require.NoError(t, err, "parse advisory_hints.go")

	names := map[string]struct{}{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if strings.HasPrefix(n.Name, "advHint") {
					names[n.Name] = struct{}{}
				}
			}
		}
	}
	require.NotEmpty(t, names, "advisory_hints.go must declare advHint* consts")
	return names
}
