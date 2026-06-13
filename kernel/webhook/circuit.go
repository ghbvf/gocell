package webhook

import (
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/circuitbreaker"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// noTenantSentinel is the breaker-key tenant segment for a tenantless (system)
// delivery — an outbox entry whose principal carries no TenantID. Mirrors the
// idempotency/reconcile _notenant convention so a tenantless delivery can never
// silently share a breaker with a real tenant.
const noTenantSentinel = "_notenant"

// circuitEndpointKey identifies one circuit breaker: the (tenant, endpoint)
// pair. Distinct tenants delivering to the SAME target URL get INDEPENDENT
// breakers, so one tenant's failures cannot fast-fail another tenant's
// deliveries to a shared endpoint (#2102 F1). The MDM / zero-trust posture
// treats a tenant-blind shared breaker as a cross-tenant denial-of-service
// vector, so tenant is part of breaker identity, not just endpoint.
type circuitEndpointKey struct {
	tenant   string
	endpoint string
}

// newCircuitEndpointKey builds the key, substituting the _notenant sentinel for
// a tenantless delivery so it occupies its own isolated breaker namespace.
func newCircuitEndpointKey(tenant, endpoint string) circuitEndpointKey {
	if tenant == "" {
		tenant = noTenantSentinel
	}
	return circuitEndpointKey{tenant: tenant, endpoint: endpoint}
}

// registryKey is the breaker-map key — the full (tenant, endpoint) identity, so
// the registry isolates per tenant AND per endpoint. The NUL separator cannot
// appear in a tenant SafeID or a URL, so the two segments are unambiguous.
func (k circuitEndpointKey) registryKey() string {
	return k.tenant + "\x00" + k.endpoint
}

// logName is the operator-facing breaker label emitted in state-transition logs
// (kernel/circuitbreaker fireTransitions "name" attr). It is the endpoint host
// plus a short fingerprint of the full key: no raw path/query (which may carry
// tenant identifiers or secret hints), yet distinct per endpoint AND per tenant
// so an operator can tell apart two breakers that share a host (#2102 F2).
func (k circuitEndpointKey) logName() string {
	host := k.endpoint
	if u, err := url.Parse(k.endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	return host + "#" + circuitFingerprint(k.registryKey())
}

// circuitFingerprint is a short non-cryptographic fingerprint (FNV-1a, 8 hex)
// used only to disambiguate breaker log labels. A collision would only blur a
// log label; it never affects isolation, which keys on the full registryKey.
func circuitFingerprint(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

// Circuit-breaker defaults for outbound webhook delivery. These mirror the
// sony/gobreaker standard defaults but are stated explicitly here so the
// dispatcher's resilience posture is self-documenting and decoupled from the
// kernel breaker's own zero-value defaults.
const (
	// circuitTripThreshold trips an endpoint's breaker once consecutive
	// delivery failures EXCEED this count (so the threshold+1'th failure opens).
	circuitTripThreshold = 5
	// circuitOpenTimeout is how long an endpoint stays open (fast-failing)
	// before the breaker admits a single half-open probe.
	circuitOpenTimeout = 60 * time.Second
	// circuitHalfOpenProbes is the number of probe deliveries admitted while
	// half-open; a single probe is enough to decide recovery.
	circuitHalfOpenProbes = 1
	// circuitGateMaxEndpoints bounds the per-endpoint breaker registry so a
	// selector that fans out to unbounded distinct target URLs cannot grow
	// memory without limit (DoS guard). Mirrors kernel/reconcile/backoff.go's
	// maxBackoffEntries. At this cardinality eviction is a pathological safety
	// valve, not a hot path.
	circuitGateMaxEndpoints = 1024
)

// circuitReadyToTrip opens the breaker once consecutive failures exceed
// circuitTripThreshold. Package-level so every per-endpoint breaker shares one
// function value rather than allocating a closure per endpoint.
func circuitReadyToTrip(c circuitbreaker.Counts) bool {
	return c.ConsecutiveFailures > circuitTripThreshold
}

// errCircuitProbeFailure is the sentinel handed to a breaker's done callback
// when a delivery attempt counts as an endpoint-HEALTH failure. The breaker's
// default IsSuccessful (err == nil → success) then records it as a failure.
var errCircuitProbeFailure = errors.New("webhook: endpoint health failure")

// circuitProbeOutcome maps a delivery attempt to the breaker done(err) value:
// nil = success, errCircuitProbeFailure = failure. It returns failure ONLY for
// endpoint-health signals — a transport fault (timeout / connection refused /
// DNS failure, but NOT an SSRF block, which is a config/policy issue rather
// than an unhealthy endpoint), a 5xx, or a 429. A 2xx and every other 4xx mean
// the endpoint is reachable and responding, so they are breaker successes even
// when the outbox still Requeues the delivery (Classify is independent — the
// breaker tracks endpoint reachability, not payload validity).
// ref: ADR 202606140035-1541 §D3
func circuitProbeOutcome(statusCode int, transportErr error) error {
	if transportErr != nil {
		var ee *errcode.Error
		if errors.As(transportErr, &ee) && ee.Code == errcode.ErrWebhookSSRFBlocked {
			return nil // SSRF block is not an endpoint-health signal.
		}
		return errCircuitProbeFailure // timeout / connection refused / DNS failure
	}
	if statusCode >= 500 || statusCode == http.StatusTooManyRequests {
		return errCircuitProbeFailure
	}
	return nil // 2xx and other 4xx: the endpoint is up.
}

// circuitGate is the per-(tenant, endpoint) circuit-breaker registry a
// Dispatcher gates outbound delivery on. Each (tenant, target URL) gets its own
// Breaker so one unhealthy receiver — or one tenant's failures against a shared
// URL — does not fast-fail deliveries from other tenants or to healthy targets.
// The map is bounded (circuitGateMaxEndpoints) as a DoS guard.
type circuitGate struct {
	clk      clock.Clock
	mu       sync.Mutex
	breakers map[string]*circuitbreaker.Breaker
}

// newCircuitGate builds an enabled circuit gate. clk is the dispatcher's clock,
// shared so open→half-open transitions advance with the same time source the
// tests drive.
func newCircuitGate(clk clock.Clock) *circuitGate {
	clock.MustHaveClock(clk, "webhook.newCircuitGate")
	return &circuitGate{clk: clk, breakers: make(map[string]*circuitbreaker.Breaker)}
}

// Allow gates a delivery for key. It returns allowed=true and a done callback
// (call exactly once with the circuitProbeOutcome) when the (tenant, endpoint)
// circuit is closed or admits a half-open probe; allowed=false and a nil done
// when the circuit is open.
func (g *circuitGate) Allow(key circuitEndpointKey) (allowed bool, done func(err error)) {
	b := g.breakerFor(key)
	if b == nil {
		// Unreachable: breakerFor returns nil only if breaker construction
		// failed, which happens solely on an empty Name — and logName always
		// yields a non-empty "<host>#<fingerprint>". Kept as a defensive,
		// lint-required handling of New's error: fail open so a hypothetical
		// construction bug degrades to "no breaker protection", never to
		// "delivery blocked". Log at Error (correctness failure).
		slog.Error("webhook: circuit breaker construction failed, failing open",
			slog.String("warning", "endpoint circuit breaker unavailable"))
		return true, func(error) {}
	}
	return b.Allow()
}

// size reports the number of tracked breakers (test seam for the bounded-
// registry invariant).
func (g *circuitGate) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.breakers)
}

// breakerFor returns the breaker for key, lazily constructing one. When the
// registry is at capacity it evicts an arbitrary entry first (DoS safety valve):
// at circuitGateMaxEndpoints distinct (tenant, endpoint) pairs the victim choice
// is not correctness-critical — a re-observed pair simply rebuilds its breaker,
// the worst case being one lost open-state that re-trips on the next failure burst.
func (g *circuitGate) breakerFor(key circuitEndpointKey) *circuitbreaker.Breaker {
	g.mu.Lock()
	defer g.mu.Unlock()
	rk := key.registryKey()
	if b, ok := g.breakers[rk]; ok {
		return b
	}
	if len(g.breakers) >= circuitGateMaxEndpoints {
		for k := range g.breakers { // evict one arbitrary entry
			delete(g.breakers, k)
			break
		}
	}
	b, err := circuitbreaker.New(circuitbreaker.Config{
		Name:        key.logName(), // safe log label: host#fingerprint, no path/query
		MaxRequests: circuitHalfOpenProbes,
		Timeout:     circuitOpenTimeout,
		ReadyToTrip: circuitReadyToTrip,
	}, g.clk)
	if err != nil {
		return nil
	}
	g.breakers[rk] = b
	return b
}
