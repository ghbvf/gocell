package cell_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
)

// testDoublePolicy is a test-local implementation of cell.Policy that is
// constructed entirely within the kernel/cell_test package, without any
// dependency on runtime/bootstrap. This confirms that the cell.Policy interface
// is satisfiable independently of runtime code.
type testDoublePolicy struct {
	description string
}

func (p testDoublePolicy) Describe() string { return p.description }

func TestPolicyInterface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		policy    cell.Policy
		wantDescr string
	}{
		{
			name:      "basic describe",
			policy:    testDoublePolicy{description: "test-policy"},
			wantDescr: "test-policy",
		},
		{
			name:      "empty description",
			policy:    testDoublePolicy{description: ""},
			wantDescr: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.policy.Describe(); got != tc.wantDescr {
				t.Errorf("Describe() = %q, want %q", got, tc.wantDescr)
			}
		})
	}
}
