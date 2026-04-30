package cell_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
)

func TestListenerRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ref        cell.ListenerRef
		wantString string
		wantIsZero bool
	}{
		{
			name:       "zero value IsZero true and String empty",
			ref:        cell.ListenerRef{},
			wantString: "",
			wantIsZero: true,
		},
		{
			name:       "PrimaryListener",
			ref:        cell.PrimaryListener,
			wantString: "primary",
			wantIsZero: false,
		},
		{
			name:       "InternalListener",
			ref:        cell.InternalListener,
			wantString: "internal",
			wantIsZero: false,
		},
		{
			name:       "HealthListener",
			ref:        cell.HealthListener,
			wantString: "health",
			wantIsZero: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.ref.String(); got != tc.wantString {
				t.Errorf("String() = %q, want %q", got, tc.wantString)
			}
			if got := tc.ref.IsZero(); got != tc.wantIsZero {
				t.Errorf("IsZero() = %v, want %v", got, tc.wantIsZero)
			}
		})
	}
}
