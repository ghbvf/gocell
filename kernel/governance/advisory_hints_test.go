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
// every long advisory hint. The 13 constants in advisory_hints.go are product
// value (greppable diagnostic strings); silent rewording would change CLI
// output that operators have wired into runbooks/grep filters.
//
// Update procedure when intentionally changing a hint:
//  1. Run `go test ./kernel/governance/... -update` (manually update golden).
//  2. Review the diff in testdata/advisory_hints.golden.txt as a code change.
//  3. Document the rationale in the PR description.
func TestAdvisoryHints_Golden(t *testing.T) {
	hints := map[string]string{
		"advHintADV05EmptySubscribers":  advHintADV05EmptySubscribers,
		"advHintADV06ContractToSlice":   advHintADV06ContractToSlice,
		"advHintADV06SliceToContract":   advHintADV06SliceToContract,
		"advHintCH04CorrelationFailed":  advHintCH04CorrelationFailed,
		"advHintCH05CorrelationFailed":  advHintCH05CorrelationFailed,
		"advHintCH05MissingParseCall":   advHintCH05MissingParseCall,
		"advHintFMT13MissingHTTP":       advHintFMT13MissingHTTP,
		"advHintFMT13MissingPathParam":  advHintFMT13MissingPathParam,
		"advHintCCE01TriggerNotEvent":   advHintCCE01TriggerNotEvent,
		"advHintCCE01OwnerMismatch":     advHintCCE01OwnerMismatch,
		"advHintCCE01SliceNotPublish":   advHintCCE01SliceNotPublish,
		"advHintCCE01TriggerNotEmitted": advHintCCE01TriggerNotEmitted,
		"advHintCCE01ReverseEmit":       advHintCCE01ReverseEmit,
	}
	require.Len(t, hints, 13, "advisory_hints.go must have exactly 13 promoted constants — update this test if the count changes")

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
	want, err := os.ReadFile(goldenPath)
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
