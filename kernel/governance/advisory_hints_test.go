package governance

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAdvisoryHints_Golden locks the canonical text + ID prefix coverage of
// every long advisory hint. The 55 constants in advisory_hints.go are product
// value (greppable diagnostic strings); silent rewording would change CLI
// output that operators have wired into runbooks/grep filters.
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
		// ADV-06
		"advHintADV06ContractToSlice":    advHintADV06ContractToSlice,
		"advHintADV06ContractToSliceFix": advHintADV06ContractToSliceFix,
		"advHintADV06SliceToContract":    advHintADV06SliceToContract,
		"advHintADV06SliceToContractFix": advHintADV06SliceToContractFix,
		// CH-04
		"advHintCH04CorrelationFailed":    advHintCH04CorrelationFailed,
		"advHintCH04CorrelationFailedFix": advHintCH04CorrelationFailedFix,
		// CH-05
		"advHintCH05CorrelationFailed":    advHintCH05CorrelationFailed,
		"advHintCH05CorrelationFailedFix": advHintCH05CorrelationFailedFix,
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
	require.Len(t, hints, 55, "advisory_hints.go must have exactly 55 promoted constants — update this test if the count changes")

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
