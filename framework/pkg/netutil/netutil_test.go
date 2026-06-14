package netutil_test

import (
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/netutil"
)

func TestIsValidNetworkAddress(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		// host:port forms
		{name: "bare host:port", input: "host:8080", want: true},
		{name: "localhost:port", input: "localhost:9000", want: true},
		{name: "loopback IP:port", input: "127.0.0.1:8080", want: true},
		{name: "IPv6:port", input: "[::1]:9000", want: true},

		// http/https URL forms
		{name: "https URL with host", input: "https://remote.svc/", want: true},
		{name: "http URL with host", input: "http://cell-d.internal", want: true},
		{name: "https URL with port", input: "https://host:9443/api", want: true},

		// rejected schemes
		{name: "grpc:// rejected", input: "grpc://h:1", want: false},
		{name: "file:// rejected", input: "file:///tmp/sock", want: false},
		{name: "gopher:// rejected", input: "gopher://example.com", want: false},

		// empty / whitespace
		{name: "empty string", input: "", want: false},
		{name: "whitespace only", input: "   ", want: false},

		// malformed
		{name: "bare hostname (no port)", input: "hostname", want: false},
		{name: "not a valid addr", input: "not-a-valid-addr", want: false},
		{name: "bad URL no host", input: "://bad", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := netutil.IsValidNetworkAddress(tc.input)
			if got != tc.want {
				t.Errorf("IsValidNetworkAddress(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
