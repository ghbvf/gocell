package hello

import "testing"

// TestService_Greeting verifies the hello slice's pure-compute business logic
// (the L0 unit referenced by slice.yaml verify.unit: unit.hello.service).
func TestService_Greeting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want string
	}{
		{name: "fixed greeting", want: "hello, gocell"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, err := NewService()
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			if got := svc.Greeting(); got != tc.want {
				t.Errorf("Greeting() = %q, want %q", got, tc.want)
			}
		})
	}
}
