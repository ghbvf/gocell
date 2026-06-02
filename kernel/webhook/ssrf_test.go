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

// userinfoTestURL embeds userinfo (user:pass@) to prove ValidateTargetURL
// rejects it (F10). Declared as a const so the gosec G101 suppression stays on a
// short line instead of bloating the table row past the lll limit.
const userinfoTestURL = "https://user:pass@hooks.example.com/v1" //nolint:gosec // G101: deliberate test fixture, not a real credential

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
	p := NewSafePolicy()
	for _, cidr := range ssrfBlockedCIDRStrings {
		host := strings.SplitN(cidr, "/", 2)[0] // network address is contained in the CIDR
		t.Run(cidr, func(t *testing.T) {
			t.Parallel()
			_, err := p.DialContext(context.Background(), "tcp", net.JoinHostPort(host, "443"))
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
			p := NewSafePolicy(tc.opts...)
			ctx := context.Background()
			if !tc.wantBlocked {
				ctx = canceledContext(t) // got past vet → fail fast, no network
			}
			_, err := p.DialContext(ctx, "tcp", net.JoinHostPort(tc.host, "443"))
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
		// Note: resolver-error / empty-answer cases moved to
		// TestSafeDialer_ResolutionFailure_Transient — those are fail-closed at
		// the dial layer but TRANSIENT (ErrWebhookDeliveryFailed → Requeue), not
		// SSRF blocks, so they no longer belong in this isSSRFBlocked table.
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewSafePolicy(withResolver(tc.resolver))
			ctx := context.Background()
			if !tc.wantBlocked {
				ctx = canceledContext(t)
			}
			_, err := p.DialContext(ctx, "tcp", "evil.example.com:443")
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

// TestSafeDialer_ResolutionFailure_Transient (F1): a resolver FAILURE or empty
// answer set is fail-closed at the dial layer (still an error — no connection is
// made), but it carries the TRANSIENT ErrWebhookDeliveryFailed code, NOT
// ErrWebhookSSRFBlocked. Classify therefore maps it to Requeue, not a permanent
// Reject/DLX — a DNS hiccup must not dead-letter the delivery.
func TestSafeDialer_ResolutionFailure_Transient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		resolver fakeResolver
		reason   string
	}{
		{name: "resolver_error", resolver: fakeResolver{err: errors.New("dns boom")}, reason: "resolution_failed"},
		{name: "resolver_empty", resolver: fakeResolver{addrs: nil}, reason: "no_addresses"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewSafePolicy(withResolver(tc.resolver))
			_, err := p.DialContext(context.Background(), "tcp", "evil.example.com:443")
			require.Error(t, err)
			assert.Falsef(t, isSSRFBlocked(t, err),
				"DNS failure must be transient, not an SSRF block, got %v", err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrWebhookDeliveryFailed, ec.Code,
				"DNS failure must carry the transient ErrWebhookDeliveryFailed code")
			assert.Equal(t, tc.reason, internalReason(t, err))
		})
	}
}

func TestSafeDialer_MalformedAddress(t *testing.T) {
	t.Parallel()
	p := NewSafePolicy()
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
			_, err := p.DialContext(context.Background(), "tcp", tc.addr)
			require.Error(t, err)
			assert.Truef(t, isSSRFBlocked(t, err), "malformed address must fail closed as SSRF-blocked, got %v", err)
			assert.Equalf(t, "malformed_address", internalReason(t, err),
				"malformed address reject must carry stable Internal reason, got %v", err)
		})
	}
}

// internalReason extracts the stable "reason" InternalDetail from an
// errcode.Error reject path (server-only observability field, never on the
// wire). Returns "" when absent.
func internalReason(t *testing.T, err error) string {
	t.Helper()
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return ""
	}
	for _, d := range ec.InternalDetails {
		if a := d.AsSlogAttr(); a.Key == "reason" {
			return a.Value.String()
		}
	}
	return ""
}

func TestValidateTargetURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		rawURL     string
		opts       []SafeOption
		wantErr    bool
		wantReason string // stable Internal "reason" when wantErr (empty = don't assert)
	}{
		{name: "https_ok", rawURL: "https://hooks.example.com/v1", wantErr: false},
		{name: "http_ok", rawURL: "http://internal-mesh.svc/webhook", wantErr: false},
		{name: "public_ip_literal_ok", rawURL: "https://8.8.8.8/x", wantErr: false},
		{name: "ftp_blocked", rawURL: "ftp://x.example.com/f", wantErr: true, wantReason: "scheme_not_allowed"},
		{name: "file_blocked", rawURL: "file:///etc/passwd", wantErr: true, wantReason: "scheme_not_allowed"},
		{name: "gopher_blocked", rawURL: "gopher://x.example.com/", wantErr: true, wantReason: "scheme_not_allowed"},
		{name: "private_ip_literal_blocked", rawURL: "http://10.0.0.1/x", wantErr: true, wantReason: "ssrf_blocked"},
		{name: "metadata_ip_literal_blocked", rawURL: "http://169.254.169.254/latest/meta-data/", wantErr: true, wantReason: "ssrf_blocked"},
		{name: "mapped_private_literal_blocked", rawURL: "http://[::ffff:10.0.0.1]/x", wantErr: true, wantReason: "ssrf_blocked"},
		{name: "unparseable_blocked", rawURL: "ht!tp://\x7f", wantErr: true, wantReason: "unparseable"},
		// F10: embedded userinfo is rejected (it would otherwise become a Basic-Auth
		// header and leak into redirect/error logs).
		{name: "userinfo_user_pass_blocked", rawURL: userinfoTestURL, wantErr: true, wantReason: "userinfo_not_allowed"},
		{name: "userinfo_user_only_blocked", rawURL: "https://user@hooks.example.com/v1", wantErr: true, wantReason: "userinfo_not_allowed"},
		// F1: an empty host (http:///x) is un-vettable → fail closed, not silently OK.
		{name: "empty_host_blocked", rawURL: "http:///x", wantErr: true, wantReason: "empty_host"},
		// F2: loopback exemption is policy-coherent — the pre-flight honors
		// WithAllowLoopback exactly as the dial does, instead of rejecting a
		// literal loopback the dialer is configured to allow.
		{name: "loopback_literal_blocked_default", rawURL: "http://127.0.0.1:8080/", wantErr: true, wantReason: "ssrf_blocked"},
		{name: "loopback_literal_ok_with_optin", rawURL: "http://127.0.0.1:8080/", opts: []SafeOption{WithAllowLoopback()}, wantErr: false},
		{name: "ipv6_loopback_ok_with_optin", rawURL: "http://[::1]:8080/", opts: []SafeOption{WithAllowLoopback()}, wantErr: false},
		{
			name: "private_still_blocked_with_optin", rawURL: "http://10.0.0.1/x",
			opts: []SafeOption{WithAllowLoopback()}, wantErr: true, wantReason: "ssrf_blocked",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := NewSafePolicy(tc.opts...).ValidateTargetURL(tc.rawURL)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Truef(t, isSSRFBlocked(t, err), "expected SSRF-blocked, got %v", err)
			if tc.wantReason != "" {
				assert.Equalf(t, tc.wantReason, internalReason(t, err),
					"reject must carry stable Internal reason, got %v", err)
			}
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
			rerr := NewSafePolicy().DenyRedirect(req, tc.via)
			require.Error(t, rerr)
			assert.True(t, isSSRFBlocked(t, rerr))
			assert.Equal(t, "redirect_denied", internalReason(t, rerr))

			var ec *errcode.Error
			require.ErrorAs(t, rerr, &ec)
			assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)
			assert.Equal(t, http.StatusForbidden, ec.Status())
		})
	}
}

// TestDenyRedirect_RedactsUserinfo (F10): DenyRedirect records the source URL in
// the server-side diagnostic; any userinfo password must be masked (url.Redacted)
// so it cannot leak into slog.
func TestDenyRedirect_RedactsUserinfo(t *testing.T) {
	t.Parallel()
	src, err := http.NewRequest(http.MethodGet, "https://user:supersecret@x.example.com/hook", nil)
	require.NoError(t, err)

	rerr := NewSafePolicy().DenyRedirect(src, []*http.Request{src})
	require.Error(t, rerr)

	var ec *errcode.Error
	require.ErrorAs(t, rerr, &ec)
	from := internalAttrValue(t, ec, "from_url")
	assert.NotContains(t, from, "supersecret", "userinfo password must be redacted from the diagnostic")
	assert.Contains(t, from, "xxxxx", "url.Redacted masks the password as xxxxx")
}

// TestNormalizeIP locks the IPv4-mapped dewrap behavior in isolation.
func TestNormalizeIP(t *testing.T) {
	t.Parallel()
	assert.Equal(t, net.ParseIP("10.0.0.1").To4(), normalizeIP(net.ParseIP("::ffff:10.0.0.1")))
	assert.Equal(t, net.ParseIP("8.8.8.8").To4(), normalizeIP(net.ParseIP("8.8.8.8")))
	v6 := net.ParseIP("2606:4700::1111")
	assert.Equal(t, v6, normalizeIP(v6))
}
