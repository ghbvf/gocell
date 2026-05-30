package postgres

import (
	"testing"

	kout "github.com/ghbvf/gocell/kernel/outbox"
)

// TestDecodeOversizeGuardedJSONB exercises the five outcomes of the shared
// size-capped JSONB decode helper directly. The scanClaimedEntry call sites
// only reach it through a live DB row, so without this the drop branches
// (empty / oversize / unmarshal-error / validate-error) were exercised only by
// the postgres integration suite.
func TestDecodeOversizeGuardedJSONB(t *testing.T) {
	const big = 1 << 20
	tests := []struct {
		name     string
		raw      string
		maxBytes int
		wantOK   bool
	}{
		{"empty bytes skip without decode", "", big, false},
		// len 19 > cap 4 → oversize branch, never parsed.
		{"oversize dropped", `{"traceParent":"x"}`, 4, false},
		{"invalid json dropped", `{`, big, false},
		// Decodes, but a non-W3C traceParent fails ObservabilityMetadata.Validate.
		{"valid json failing Validate dropped", `{"traceParent":"not-a-valid-traceparent"}`, big, false},
		{"valid empty object accepted", `{}`, big, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := decodeOversizeGuardedJSONB[kout.ObservabilityMetadata](
				[]byte(tt.raw), tt.maxBytes, observabilityDecodeWarnings, "evt-1", "test.event")
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok && got != (kout.ObservabilityMetadata{}) {
				t.Errorf("a rejected decode must leave the zero value, got %+v", got)
			}
		})
	}
}

// TestDecodeOversizeGuardedJSONB_PrincipalType locks the generic helper against
// the second concrete column type (PrincipalMetadata) so a Validate-constraint
// or type-inference regression on either identity column surfaces.
func TestDecodeOversizeGuardedJSONB_PrincipalType(t *testing.T) {
	got, ok := decodeOversizeGuardedJSONB[kout.PrincipalMetadata](
		[]byte(`{}`), 1<<20, principalDecodeWarnings, "evt-1", "test.event")
	if !ok {
		t.Fatalf("valid empty principal: ok = false, want true")
	}
	if got != (kout.PrincipalMetadata{}) {
		t.Errorf("empty principal decode = %+v, want zero value", got)
	}
}
