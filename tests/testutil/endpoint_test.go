package testutil

import "testing"

func TestLoopbackIPEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "hostport", in: "localhost:32782", want: "127.0.0.1:32782"},
		{name: "url", in: "http://localhost:32807", want: "http://127.0.0.1:32807"},
		{name: "redis url", in: "redis://localhost:32782/0", want: "redis://127.0.0.1:32782/0"},
		{name: "ip literal unchanged", in: "http://127.0.0.1:8200", want: "http://127.0.0.1:8200"},
		{name: "remote unchanged", in: "https://vault.example.test:8200", want: "https://vault.example.test:8200"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := LoopbackIPEndpoint(tt.in); got != tt.want {
				t.Fatalf("LoopbackIPEndpoint(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
