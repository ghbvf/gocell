package cell_test

import (
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
)

func TestPolicyIsZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy cell.Policy
		want   bool
	}{
		{
			name:   "zero value is zero",
			policy: cell.Policy{},
			want:   true,
		},
		{
			name:   "name only is not zero",
			policy: cell.Policy{Name: "some-policy"},
			want:   false,
		},
		{
			name: "middleware only is not zero",
			policy: cell.Policy{
				Middleware: func(next http.Handler) http.Handler { return next },
			},
			want: false,
		},
		{
			name: "name and middleware is not zero",
			policy: cell.Policy{
				Name:       "full-policy",
				Middleware: func(next http.Handler) http.Handler { return next },
			},
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.policy.IsZero(); got != tc.want {
				t.Errorf("IsZero() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPolicyName(t *testing.T) {
	t.Parallel()

	p := cell.Policy{Name: "test-policy"}
	if got := p.Name; got != "test-policy" {
		t.Errorf("Name = %q, want %q", got, "test-policy")
	}
}
