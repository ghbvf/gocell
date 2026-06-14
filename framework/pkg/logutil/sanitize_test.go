package logutil

import (
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain ascii", "hello world", "hello world"},
		{"strip newline", "a\nb", "ab"},
		{"strip cr", "a\rb", "ab"},
		{"strip bell", "a\x07b", "ab"},
		{"strip esc", "a\x1bb", "ab"},
		{"strip del", "a\x7fb", "ab"},
		{"strip c1 control", "a\u0085b", "ab"},
		{"strip unicode line separator", "a\u2028b", "ab"},
		{"strip unicode paragraph separator", "a\u2029b", "ab"},
		{"keep unicode", "héllo 🚀", "héllo 🚀"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSafeAddr(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"ipv4", "1.2.3.4:5678", "1.2.3.4:5678"},
		{"ipv6", "[::1]:80", "[::1]:80"},
		{"hostname", "example.com:443", "example.com:443"},
		{"strip control on parse failure", "bad\nvalue", "badvalue"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SafeAddr(tc.in)
			if got != tc.want {
				t.Fatalf("SafeAddr(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
