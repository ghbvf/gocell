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

		// http/https URL forms (scheme+host[:port] only; optional root "/")
		{name: "https URL with host + root slash", input: "https://remote.svc/", want: true},
		{name: "http URL with host", input: "http://cell-d.internal", want: true},
		{name: "https URL with port (no path)", input: "https://host:9443", want: true},

		// path/query/fragment rejected — endpoint must not carry these (#1966 P2.9):
		// the remote transport keeps only scheme+host, so they would be silently dropped.
		{name: "https URL with path rejected", input: "https://host:9443/api", want: false},
		{name: "http URL with deep path rejected", input: "http://cell-d.internal/v1/x", want: false},
		{name: "https URL with query rejected", input: "https://host:9443/?k=v", want: false},
		{name: "http URL with fragment rejected", input: "http://host:9443/#frag", want: false},
		{name: "bare host:port with path rejected", input: "host:8080/foo", want: false},

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
