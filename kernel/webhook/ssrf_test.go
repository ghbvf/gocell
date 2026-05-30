package webhook

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// fakeResolver returns a fixed set of addresses regardless of host. It drives
// the DNS-rebinding tests: a public-looking hostname can be made to "resolve"
// to a private IP, which the dial-time vet must reject before connecting.
type fakeResolver struct {
	addrs []net.IPAddr
	err   error
}

func (f fakeResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	return f.addrs, f.err
}

func ipAddrs(ips ...string) []net.IPAddr {
	out := make([]net.IPAddr, 0, len(ips))
	for _, s := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out
}

// isSSRFBlocked reports whether err carries the ErrWebhookSSRFBlocked code (a
// KindPermissionDenied → 403 errcode.Error).
func isSSRFBlocked(t *testing.T, err error) bool {
	t.Helper()
	var ec *errcode.Error
	return errors.As(err, &ec) && ec.Code == errcode.ErrWebhookSSRFBlocked
}

// canceledContext returns a context that is already canceled, so a dial that
// passes the SSRF vet fails fast at the transport layer (no real network I/O)
// rather than hanging on a real connection attempt.
func canceledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestSafeDialer_BlocksEveryBlocklistCIDR drives one case per blocklist entry:
// the network address of each CIDR must be rejected. Driven from
// ssrfBlockedCIDRStrings so a newly added CIDR is automatically covered.
func TestSafeDialer_BlocksEveryBlocklistCIDR(t *testing.T) {
	t.Parallel()
	dial := NewSafeDialer()
	for _, cidr := range ssrfBlockedCIDRStrings {
		host := strings.SplitN(cidr, "/", 2)[0] // network address is contained in the CIDR
		t.Run(cidr, func(t *testing.T) {
			t.Parallel()
			_, err := dial(context.Background(), "tcp", net.JoinHostPort(host, "443"))
			require.Error(t, err)
			assert.Truef(t, isSSRFBlocked(t, err), "expected SSRF-blocked for %s, got %v", host, err)
		})
	}
}

func TestSafeDialer_IPLiteralCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		host        string
		opts        []SafeOption
		wantBlocked bool
	}{
		{name: "cloud_metadata", host: "169.254.169.254", wantBlocked: true},
		{name: "private_10", host: "10.0.0.5", wantBlocked: true},
		{name: "loopback_default", host: "127.0.0.1", wantBlocked: true},
		{name: "ipv6_loopback_default", host: "::1", wantBlocked: true},
		{name: "ula_v6", host: "fc00::1", wantBlocked: true},
		{name: "mapped_private", host: "::ffff:10.0.0.1", wantBlocked: true},
		{name: "mapped_metadata", host: "::ffff:169.254.169.254", wantBlocked: true},
		// Public addresses pass the vet (and then fail fast on the canceled ctx).
		{name: "public_v4", host: "8.8.8.8", wantBlocked: false},
		{name: "public_v4_cloudflare", host: "1.1.1.1", wantBlocked: false},
		{name: "public_v6", host: "2606:4700::1111", wantBlocked: false},
		// A PUBLIC IPv4 wrapped as IPv4-mapped IPv6 must NOT be over-blocked —
		// proves normalizeIP dewraps before the blocklist check (and that we
		// correctly excluded ::ffff:0:0/96).
		{name: "mapped_public", host: "::ffff:8.8.8.8", wantBlocked: false},
		// Loopback opt-in: only loopback is exempted, not other private ranges.
		{name: "loopback_optin_allowed", host: "127.0.0.1", opts: []SafeOption{WithAllowLoopback()}, wantBlocked: false},
		{name: "ipv6_loopback_optin_allowed", host: "::1", opts: []SafeOption{WithAllowLoopback()}, wantBlocked: false},
		{name: "private_still_blocked_with_optin", host: "10.0.0.5", opts: []SafeOption{WithAllowLoopback()}, wantBlocked: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dial := NewSafeDialer(tc.opts...)
			ctx := context.Background()
			if !tc.wantBlocked {
				ctx = canceledContext(t) // got past vet → fail fast, no network
			}
			_, err := dial(ctx, "tcp", net.JoinHostPort(tc.host, "443"))
			require.Error(t, err)
			if tc.wantBlocked {
				assert.Truef(t, isSSRFBlocked(t, err), "expected SSRF-blocked, got %v", err)
			} else {
				// Passed the vet → the only error is the canceled-ctx dial,
				// proving rejection happened at the transport, not the guard.
				assert.Falsef(t, isSSRFBlocked(t, err), "expected to pass vet (non-SSRF error), got SSRF block: %v", err)
				assert.ErrorIsf(t, err, context.Canceled, "expected canceled-ctx dial error after vet, got %v", err)
			}
		})
	}
}

func TestSafeDialer_DNSRebinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		resolver    fakeResolver
		wantBlocked bool
	}{
		{
			name:        "public_name_resolves_private",
			resolver:    fakeResolver{addrs: ipAddrs("10.0.0.5")},
			wantBlocked: true,
		},
		{
			name:        "mixed_answers_fail_closed",
			resolver:    fakeResolver{addrs: ipAddrs("1.1.1.1", "10.0.0.5")},
			wantBlocked: true,
		},
		{
			name:        "metadata_via_dns",
			resolver:    fakeResolver{addrs: ipAddrs("169.254.169.254")},
			wantBlocked: true,
		},
		{
			name:        "all_public_passes_vet",
			resolver:    fakeResolver{addrs: ipAddrs("93.184.216.34")},
			wantBlocked: false,
		},
		{
			name:        "resolver_error_fail_closed",
			resolver:    fakeResolver{err: errors.New("dns boom")},
			wantBlocked: true,
		},
		{
			name:        "resolver_empty_fail_closed",
			resolver:    fakeResolver{addrs: nil},
			wantBlocked: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dial := NewSafeDialer(withResolver(tc.resolver))
			ctx := context.Background()
			if !tc.wantBlocked {
				ctx = canceledContext(t)
			}
			_, err := dial(ctx, "tcp", "evil.example.com:443")
			require.Error(t, err)
			if tc.wantBlocked {
				assert.Truef(t, isSSRFBlocked(t, err), "expected SSRF-blocked, got %v", err)
			} else {
				assert.Falsef(t, isSSRFBlocked(t, err), "expected to pass vet, got SSRF block: %v", err)
				assert.ErrorIsf(t, err, context.Canceled, "expected canceled-ctx dial error after vet, got %v", err)
			}
		})
	}
}

func TestSafeDialer_MalformedAddress(t *testing.T) {
	t.Parallel()
	dial := NewSafeDialer()
	tests := []struct {
		name string
		addr string
	}{
		{name: "no_port", addr: "noport"},
		{name: "empty", addr: ""},
		{name: "unterminated_v6", addr: "[::1"},
		{name: "extra_colon", addr: "host:port:extra"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := dial(context.Background(), "tcp", tc.addr)
			require.Error(t, err)
			assert.Truef(t, isSSRFBlocked(t, err), "malformed address must fail closed as SSRF-blocked, got %v", err)
		})
	}
}

func TestValidateTargetURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "https_ok", rawURL: "https://hooks.example.com/v1", wantErr: false},
		{name: "http_ok", rawURL: "http://internal-mesh.svc/webhook", wantErr: false},
		{name: "public_ip_literal_ok", rawURL: "https://8.8.8.8/x", wantErr: false},
		{name: "ftp_blocked", rawURL: "ftp://x.example.com/f", wantErr: true},
		{name: "file_blocked", rawURL: "file:///etc/passwd", wantErr: true},
		{name: "gopher_blocked", rawURL: "gopher://x.example.com/", wantErr: true},
		{name: "private_ip_literal_blocked", rawURL: "http://10.0.0.1/x", wantErr: true},
		{name: "metadata_ip_literal_blocked", rawURL: "http://169.254.169.254/latest/meta-data/", wantErr: true},
		{name: "mapped_private_literal_blocked", rawURL: "http://[::ffff:10.0.0.1]/x", wantErr: true},
		{name: "unparseable_blocked", rawURL: "ht!tp://\x7f", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateTargetURL(tc.rawURL)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Truef(t, isSSRFBlocked(t, err), "expected SSRF-blocked, got %v", err)
		})
	}
}

func TestDenyRedirect(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequest(http.MethodGet, "https://x.example.com/", nil)
	require.NoError(t, err)
	req2, err := http.NewRequest(http.MethodGet, "https://y.example.com/", nil)
	require.NoError(t, err)

	// DenyRedirect must fail closed for any via chain — single, multi, and the
	// nil/empty first-redirect form net/http may pass.
	for _, tc := range []struct {
		name string
		via  []*http.Request
	}{
		{name: "single", via: []*http.Request{req}},
		{name: "multi", via: []*http.Request{req, req2}},
		{name: "empty", via: []*http.Request{}},
		{name: "nil", via: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rerr := DenyRedirect(req, tc.via)
			require.Error(t, rerr)
			assert.True(t, isSSRFBlocked(t, rerr))

			var ec *errcode.Error
			require.ErrorAs(t, rerr, &ec)
			assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)
			assert.Equal(t, http.StatusForbidden, ec.Status())
		})
	}
}

// TestNormalizeIP locks the IPv4-mapped dewrap behavior in isolation.
func TestNormalizeIP(t *testing.T) {
	t.Parallel()
	assert.Equal(t, net.ParseIP("10.0.0.1").To4(), normalizeIP(net.ParseIP("::ffff:10.0.0.1")))
	assert.Equal(t, net.ParseIP("8.8.8.8").To4(), normalizeIP(net.ParseIP("8.8.8.8")))
	v6 := net.ParseIP("2606:4700::1111")
	assert.Equal(t, v6, normalizeIP(v6))
}
