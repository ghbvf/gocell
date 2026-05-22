package healthz

import "testing"

func TestStatus_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		s    Status
		want string
	}{
		{StatusUp, "up"},
		{StatusDegraded, "degraded"},
		{StatusDown, "down"},
		{Status(255), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestStatus_Severity(t *testing.T) {
	t.Parallel()

	// Severity ordering: Up(0) < Degraded(1) < Down(2)
	if StatusUp >= StatusDegraded || StatusDegraded >= StatusDown {
		t.Errorf("severity order broken: Up=%d Degraded=%d Down=%d", StatusUp, StatusDegraded, StatusDown)
	}
}

func TestWorseStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b Status
		want Status
	}{
		{StatusUp, StatusUp, StatusUp},
		{StatusUp, StatusDegraded, StatusDegraded},
		{StatusDegraded, StatusUp, StatusDegraded},
		{StatusUp, StatusDown, StatusDown},
		{StatusDegraded, StatusDown, StatusDown},
		{StatusDown, StatusDegraded, StatusDown},
		{StatusDown, StatusDown, StatusDown},
	}
	for _, tt := range tests {
		if got := WorseStatus(tt.a, tt.b); got != tt.want {
			t.Errorf("WorseStatus(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
