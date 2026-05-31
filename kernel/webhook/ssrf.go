package webhook

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// SafeDialContext is the dial function signature shared with net.Dialer.DialContext,
// so SafePolicy.DialContext drops directly into http.Transport.DialContext (wired
// by the dispatcher in PR-5).
type SafeDialContext func(ctx context.Context, network, address string) (net.Conn, error)

// resolver is the injectable DNS seam; *net.Resolver (net.DefaultResolver)
// satisfies it. It is an unexported interface so withResolver cannot be named —
// let alone called — from outside this package: the DNS-rebinding tests inject a
// fake from within package webhook, while production has no resolver knob.
type resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// SafePolicy is the single source of webhook outbound SSRF policy. Its three
// methods — DialContext (resolve→vet→dial-literal), ValidateTargetURL (cheap
// pre-flight) and DenyRedirect (redirect deny-all) — all share one config, so a
// policy knob (e.g. WithAllowLoopback) applies identically to the pre-flight and
// the dial. PR-5 holds one *SafePolicy and wires all three into an *http.Client;
// there is no divergent per-surface vetting path.
//
// DialContext resolves the host, vets every resolved IP against the SSRF
// blocklist, then dials a vetted IP literal — closing the DNS-rebinding (TOCTOU)
// window because the connection target is the already-checked literal, never a
// re-resolved hostname.
//
// ref: stripe/smokescreen pkg/smokescreen (resolve→vet→dial-literal)
// ref: stealthrocket/netjail security.go Rules.DialFunc
// ref: doyensec/safeurl ip.go privateNetworks (CIDR superset; Control mode not used)
type SafePolicy struct {
	allowLoopback bool
	resolver      resolver
	dialer        *net.Dialer
	// blockedNets is the shared, read-only package slice (ssrfBlockedNets).
	// It must not be mutated through this field — there is no option that
	// rewrites it, and elements are shared *net.IPNet pointers.
	blockedNets []*net.IPNet
}

// SafeOption customizes a SafePolicy at construction time.
type SafeOption func(*SafePolicy)

// WithAllowLoopback permits loopback (127.0.0.0/8, ::1) dial AND pre-flight
// targets. It is for dev / CI only (e.g. testcontainers) and must never be set
// in production. It exempts ONLY loopback — every other private/reserved range
// stays blocked, on both the DialContext and ValidateTargetURL paths.
func WithAllowLoopback() SafeOption {
	return func(p *SafePolicy) { p.allowLoopback = true }
}

// withResolver injects a DNS resolver. Unexported (and typed on the unexported
// resolver interface) so it is a same-package-test-only seam — production code
// cannot reach it, so there is no public surface to misuse and no fallback path.
func withResolver(r resolver) SafeOption {
	return func(p *SafePolicy) { p.resolver = r }
}

// NewSafePolicy returns a SafePolicy that blocks any address resolving into the
// SSRF blocklist (see ssrfBlockedCIDRStrings).
func NewSafePolicy(opts ...SafeOption) *SafePolicy {
	p := &SafePolicy{
		resolver:    net.DefaultResolver,
		dialer:      &net.Dialer{},
		blockedNets: ssrfBlockedNets,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// DialContext is the SafeDialContext-compatible dial: resolve→vet-all→dial the
// vetted IP literal. It is the SOLE sanctioned outbound callsite for
// net.Dialer.DialContext in kernel/webhook (WEBHOOK-SSRF-GUARD-01/A2).
func (p *SafePolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked,
			"webhook: malformed dial address", err,
			errcode.WithInternal(
				errcode.InternalAttr("reason", "malformed_address"),
				errcode.InternalAttr("address", address)))
	}

	// IP literal: vet directly, no DNS.
	if ip := net.ParseIP(host); ip != nil {
		if verr := p.vet(host, ip); verr != nil {
			return nil, verr
		}
		return p.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}

	// Hostname: resolve once, vet ALL answers (fail-closed if any is blocked),
	// then dial the first vetted IP literal. No re-resolution → rebinding-proof.
	addrs, err := p.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked,
			"webhook: dial host resolution failed", err,
			errcode.WithInternal(
				errcode.InternalAttr("reason", "resolution_failed"),
				errcode.InternalAttr("host", host)))
	}
	if len(addrs) == 0 {
		return nil, errcode.New(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked,
			"webhook: dial host resolved to no addresses",
			errcode.WithInternal(
				errcode.InternalAttr("reason", "no_addresses"),
				errcode.InternalAttr("host", host)))
	}
	for _, a := range addrs {
		if verr := p.vet(host, a.IP); verr != nil {
			return nil, verr
		}
	}
	vetted := addrs[0].IP
	return p.dialer.DialContext(ctx, network, net.JoinHostPort(vetted.String(), port))
}

// vet rejects ip when it falls in any blocked CIDR. The IP is normalized via
// To4() first so IPv4-mapped IPv6 (::ffff:10.0.0.1) is checked as its embedded
// IPv4. Loopback is exempted only when allowLoopback is set. host (the target
// before resolution) is recorded server-side for incident triage; the reject
// "reason" is an Internal (server-only) attribute, never on the wire, so the 403
// does not advertise which targets the SSRF policy blocks. Shared by DialContext
// (resolved IPs) and ValidateTargetURL (literal pre-flight) so the policy is
// single-source.
func (p *SafePolicy) vet(host string, ip net.IP) error {
	norm := normalizeIP(ip)
	if p.allowLoopback && norm.IsLoopback() {
		return nil
	}
	for _, n := range p.blockedNets {
		if n.Contains(norm) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked,
				"webhook: target address is blocked by SSRF policy",
				errcode.WithInternal(
					errcode.InternalAttr("reason", "ssrf_blocked"),
					errcode.InternalAttr("host", host),
					errcode.InternalAttr("ip", norm.String())))
		}
	}
	return nil
}

// ValidateTargetURL is a cheap pre-flight: it enforces the {http, https} scheme
// allowlist, rejects an empty host (fail-closed — an un-vettable target), and
// rejects a target whose host is an IP literal already blocked by the SSRF
// policy. The IP-literal check reuses vet, so WithAllowLoopback exempts loopback
// here exactly as it does at dial time. Hostnames are still vetted at dial time
// by DialContext (resolving here would create a TOCTOU window).
func (p *SafePolicy) ValidateTargetURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errcode.Wrap(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked,
			"webhook: dispatch target URL is not parseable", err,
			errcode.WithInternal(errcode.InternalAttr("reason", "unparseable")))
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked,
			"webhook: dispatch target scheme is not allowed",
			errcode.WithInternal(
				errcode.InternalAttr("reason", "scheme_not_allowed"),
				errcode.InternalAttr("scheme", u.Scheme),
				errcode.InternalAttr("target_host", u.Hostname())))
	}
	host := u.Hostname() // strips port and IPv6 brackets
	if host == "" {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked,
			"webhook: dispatch target URL has no host",
			errcode.WithInternal(
				errcode.InternalAttr("reason", "empty_host"),
				errcode.InternalAttr("scheme", u.Scheme)))
	}
	if ip := net.ParseIP(host); ip != nil {
		return p.vet(host, ip)
	}
	return nil
}

// DenyRedirect is an http.Client.CheckRedirect that refuses ALL redirects. A 3xx
// from a vetted target could otherwise bounce egress to an unvetted (internal)
// Location; webhook delivery is one-shot, so any redirect is rejected
// (matching Stripe / GitHub webhook delivery semantics). It is a method so the
// SSRF policy surface stays coherent, though redirect deny-all needs no config.
func (p *SafePolicy) DenyRedirect(_ *http.Request, via []*http.Request) error {
	from := ""
	if n := len(via); n > 0 && via[n-1] != nil && via[n-1].URL != nil {
		from = via[n-1].URL.String()
	}
	return errcode.New(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked,
		"webhook: redirects are not permitted on outbound delivery",
		errcode.WithInternal(
			errcode.InternalAttr("reason", "redirect_denied"),
			errcode.InternalAttr("redirect_chain_len", len(via)),
			errcode.InternalAttr("from_url", from)))
}

// normalizeIP dewraps an IPv4-mapped IPv6 address (::ffff:a.b.c.d) to its 4-byte
// IPv4 form so the IPv4 blocklist entries match. Non-mapped addresses are
// returned unchanged. This is the SOLE IPv4-mapped defense — the blocklist
// deliberately omits ::ffff:0:0/96 (which would be dead after this dewrap, or
// would over-block public mapped addresses without it).
func normalizeIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

// ssrfBlockedCIDRStrings is the authoritative SSRF dial-time blocklist and the
// SINGLE source of truth, cross-checked against testdata/webhook-ssrf-deny.yaml
// by ssrf_fixtures_test.go (drift guard). It is the IETF-reserved-range superset
// from doyensec/safeurl ip.go, minus ::ffff:0:0/96 (handled by normalizeIP).
var ssrfBlockedCIDRStrings = []string{
	// ── IPv4 ──
	"0.0.0.0/8",          // unspecified / this-host
	"10.0.0.0/8",         // RFC1918 private
	"100.64.0.0/10",      // RFC6598 CGNAT
	"127.0.0.0/8",        // loopback
	"169.254.0.0/16",     // RFC3927 link-local — INCLUDES 169.254.169.254 cloud metadata
	"172.16.0.0/12",      // RFC1918 private
	"192.0.0.0/24",       // IETF protocol assignments
	"192.0.2.0/24",       // TEST-NET-1 documentation
	"192.88.99.0/24",     // 6to4 relay anycast (deprecated)
	"192.168.0.0/16",     // RFC1918 private
	"198.18.0.0/15",      // benchmarking
	"198.51.100.0/24",    // TEST-NET-2 documentation
	"203.0.113.0/24",     // TEST-NET-3 documentation
	"224.0.0.0/4",        // multicast
	"240.0.0.0/4",        // reserved
	"255.255.255.255/32", // limited broadcast
	// ── IPv6 ──
	"::/128",         // unspecified
	"::1/128",        // loopback
	"64:ff9b::/96",   // RFC6052 NAT64 well-known prefix
	"64:ff9b:1::/48", // RFC8215 NAT64 local-use prefix (embeds arbitrary IPv4 incl. private)
	"100::/64",       // discard-only
	"2001::/23",      // IETF protocol assignments (incl. Teredo 2001::/32)
	"2001:2::/48",    // benchmarking
	"2001:db8::/32",  // documentation
	"2001:10::/28",   // deprecated ORCHID
	"2001:20::/28",   // ORCHIDv2
	"2002::/16",      // 6to4
	"fc00::/7",       // RFC4193 ULA (unique local)
	"fe80::/10",      // link-local
	"ff00::/8",       // multicast
}

// ssrfBlockedNets is ssrfBlockedCIDRStrings parsed once at package init. A
// malformed literal is a programmer error (the strings are compile-time
// constants), surfaced fail-fast via the Approved panic funnel.
var ssrfBlockedNets = ssrfBlockedCIDRs()

func ssrfBlockedCIDRs() []*net.IPNet {
	out := make([]*net.IPNet, 0, len(ssrfBlockedCIDRStrings))
	for _, s := range ssrfBlockedCIDRStrings {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic(panicregister.Approved("webhook-ssrf-cidr-literal",
				errcode.Assertion("webhook: invalid SSRF CIDR literal %q: %v", s, err)))
		}
		out = append(out, n)
	}
	return out
}
