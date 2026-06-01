package correlation_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/observability/correlation"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestFromObservability verifies that FromObservability maps all fields from
// an ObservabilityMetadata struct correctly.
func TestFromObservability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		meta          outbox.ObservabilityMetadata
		wantTraceID   string
		wantRequestID string
		wantCorrID    string
	}{
		{
			name: "all fields populated",
			meta: outbox.ObservabilityMetadata{
				TraceID:       "abc123",
				RequestID:     "req456",
				CorrelationID: "corr789",
			},
			wantTraceID:   "abc123",
			wantRequestID: "req456",
			wantCorrID:    "corr789",
		},
		{
			name:          "zero value metadata maps to empty strings",
			meta:          outbox.ObservabilityMetadata{},
			wantTraceID:   "",
			wantRequestID: "",
			wantCorrID:    "",
		},
		{
			name: "only traceID set",
			meta: outbox.ObservabilityMetadata{
				TraceID: "trace-only",
			},
			wantTraceID:   "trace-only",
			wantRequestID: "",
			wantCorrID:    "",
		},
		{
			name: "only requestID set",
			meta: outbox.ObservabilityMetadata{
				RequestID: "req-only",
			},
			wantTraceID:   "",
			wantRequestID: "req-only",
			wantCorrID:    "",
		},
		{
			name: "only correlationID set",
			meta: outbox.ObservabilityMetadata{
				CorrelationID: "corr-only",
			},
			wantTraceID:   "",
			wantRequestID: "",
			wantCorrID:    "corr-only",
		},
		{
			name: "traceParent is ignored (not part of Correlation)",
			meta: outbox.ObservabilityMetadata{
				TraceID:     "t1",
				TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			wantTraceID:   "t1",
			wantRequestID: "",
			wantCorrID:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := correlation.FromObservability(tc.meta)

			if got := c.TraceID(); got != tc.wantTraceID {
				t.Errorf("TraceID() = %q, want %q", got, tc.wantTraceID)
			}
			if got := c.RequestID(); got != tc.wantRequestID {
				t.Errorf("RequestID() = %q, want %q", got, tc.wantRequestID)
			}
			if got := c.CorrelationID(); got != tc.wantCorrID {
				t.Errorf("CorrelationID() = %q, want %q", got, tc.wantCorrID)
			}
		})
	}
}

// TestZeroValueCorrelation verifies that the zero value of Correlation has
// all accessors returning empty string — no panic, no default injection.
func TestZeroValueCorrelation(t *testing.T) {
	t.Parallel()

	var c correlation.Correlation

	if got := c.TraceID(); got != "" {
		t.Errorf("zero-value TraceID() = %q, want empty string", got)
	}
	if got := c.RequestID(); got != "" {
		t.Errorf("zero-value RequestID() = %q, want empty string", got)
	}
	if got := c.CorrelationID(); got != "" {
		t.Errorf("zero-value CorrelationID() = %q, want empty string", got)
	}
}

// TestTopologyOwner verifies the Owner method on a populated Topology.
func TestTopologyOwner(t *testing.T) {
	t.Parallel()

	topo := correlation.Topology{
		"accesscore": {Team: "platform", Role: "auth"},
		"auditcore":  {Team: "platform", Role: "audit"},
	}

	tests := []struct {
		name      string
		cellID    string
		wantOwner correlation.CellOwner
		wantFound bool
	}{
		{
			name:      "hit: accesscore",
			cellID:    "accesscore",
			wantOwner: correlation.CellOwner{Team: "platform", Role: "auth"},
			wantFound: true,
		},
		{
			name:      "hit: auditcore",
			cellID:    "auditcore",
			wantOwner: correlation.CellOwner{Team: "platform", Role: "audit"},
			wantFound: true,
		},
		{
			name:      "miss: unknown cell returns zero + false",
			cellID:    "unknown",
			wantOwner: correlation.CellOwner{},
			wantFound: false,
		},
		{
			name:      "miss: empty string cellID",
			cellID:    "",
			wantOwner: correlation.CellOwner{},
			wantFound: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, found := topo.Owner(tc.cellID)
			if found != tc.wantFound {
				t.Errorf("Owner(%q) found = %v, want %v", tc.cellID, found, tc.wantFound)
			}
			if got != tc.wantOwner {
				t.Errorf("Owner(%q) = %+v, want %+v", tc.cellID, got, tc.wantOwner)
			}
		})
	}
}

// TestTopologyOwnerNilMap verifies that a nil Topology returns (zero, false)
// without panicking — fail-closed, no silent default owner.
func TestTopologyOwnerNilMap(t *testing.T) {
	t.Parallel()

	var topo correlation.Topology

	owner, found := topo.Owner("accesscore")
	if found {
		t.Errorf("nil Topology.Owner() found = true, want false")
	}
	if owner != (correlation.CellOwner{}) {
		t.Errorf("nil Topology.Owner() returned non-zero owner: %+v", owner)
	}
}

// TestTopologyEmptyMap verifies an empty (non-nil) Topology also returns
// (zero, false) for any query.
func TestTopologyEmptyMap(t *testing.T) {
	t.Parallel()

	topo := correlation.Topology{}

	owner, found := topo.Owner("accesscore")
	if found {
		t.Errorf("empty Topology.Owner() found = true, want false")
	}
	if owner != (correlation.CellOwner{}) {
		t.Errorf("empty Topology.Owner() returned non-zero owner: %+v", owner)
	}
}
