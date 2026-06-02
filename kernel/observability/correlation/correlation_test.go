package correlation_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/observability/correlation"
)

// TestNew verifies that New maps the three observability IDs correctly.
func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		traceID       string
		requestID     string
		correlationID string
	}{
		{
			name:          "all fields populated",
			traceID:       "abc123",
			requestID:     "req456",
			correlationID: "corr789",
		},
		{
			name:          "zero value maps to empty strings",
			traceID:       "",
			requestID:     "",
			correlationID: "",
		},
		{
			name:    "only traceID set",
			traceID: "trace-only",
		},
		{
			name:      "only requestID set",
			requestID: "req-only",
		},
		{
			name:          "only correlationID set",
			correlationID: "corr-only",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := correlation.New(tc.traceID, tc.requestID, tc.correlationID)

			if got := c.TraceID(); got != tc.traceID {
				t.Errorf("TraceID() = %q, want %q", got, tc.traceID)
			}
			if got := c.RequestID(); got != tc.requestID {
				t.Errorf("RequestID() = %q, want %q", got, tc.requestID)
			}
			if got := c.CorrelationID(); got != tc.correlationID {
				t.Errorf("CorrelationID() = %q, want %q", got, tc.correlationID)
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
