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

	"github.com/ghbvf/gocell/framework/kernel/circuitbreaker"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// Upper bounds for CircuitBreakerSettings fields — defense-in-depth for the
// no-disable invariant: absurdly large values make the breaker de-facto
// disabled (MaxInt TripThreshold = never trips; very large OpenTimeout =
// never recovers). These caps allow all realistic tuning ranges while
// blocking values that are semantically equivalent to "circuit breaker off".
// They are NOT performance limits.
const (
	// cbMaxTripThreshold is the upper bound for TripThreshold.
	// TripThreshold > 1000 would mean tolerating 1001+ consecutive endpoint
	// failures before tripping — effectively disabling the breaker.
	cbMaxTripThreshold = 1000
	// cbMaxOpenTimeout is the upper bound for OpenTimeout (1 hour).
	// An OpenTimeout > 1h means the breaker would stay open for a very long
	// time before admitting a probe — effectively never recovering.
	cbMaxOpenTimeout = time.Hour
	// cbMaxHalfOpenProbes is the upper bound for HalfOpenProbes.
	// HalfOpenProbes > 100 would flood a recovering endpoint with probes,
	// defeating the purpose of the half-open state.
	cbMaxHalfOpenProbes = 100
	// defaultCBOpenTimeout is the default OpenTimeout used by
	// DefaultCircuitBreakerSettings. A named const (not an inline literal) is
	// required by the PROD-DURATION-CONST invariant for production duration values.
	defaultCBOpenTimeout = 60 * time.Second
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

// circuitGateMaxEndpoints bounds the per-endpoint breaker registry so a
// selector that fans out to unbounded distinct target URLs cannot grow
// memory without limit (DoS guard). Mirrors kernel/reconcile/backoff.go's
// maxBackoffEntries. At this cardinality eviction is a pathological safety
// valve, not a hot path. Orthogonal to CB thresholds — not user-configurable.
const circuitGateMaxEndpoints = 1024

// CircuitBreakerSettings holds the per-deployment tunable thresholds for the
// outbound webhook per-endpoint circuit breaker. The zero value is invalid;
// use [DefaultCircuitBreakerSettings] to obtain valid defaults, or construct
// with all fields positive and call [CircuitBreakerSettings.Validate].
//
// Design: no Enabled/Disabled field — "disable the circuit breaker" must be
// inexpressible at the type level. Options only tune thresholds.
//
// INVARIANT: WEBHOOK-CB-NO-DISABLE-01 (Hard — reflect field freeze, see
// tools/archtest/webhook_cb_no_disable_test.go): the exported field set is
// frozen to exactly {TripThreshold int, OpenTimeout time.Duration,
// HalfOpenProbes int}. Adding an Enabled/Disabled bool or any other field that
// could semantically suppress the circuit breaker causes the archtest to fail.
//
// ref: sony/gobreaker Settings (MaxRequests/Interval/Timeout/ReadyToTrip) for
// naming inspiration; GoCell adapts to the webhook-delivery semantics.
type CircuitBreakerSettings struct {
	// TripThreshold is the number of consecutive endpoint-health failures that
	// MUST BE EXCEEDED before the breaker opens. The (TripThreshold+1)'th
	// consecutive failure opens the circuit. Example: TripThreshold=5 means the
	// 6th consecutive failure opens the circuit. Must be in [1, cbMaxTripThreshold].
	TripThreshold int
	// OpenTimeout is how long the breaker stays open (fast-failing deliveries)
	// before admitting a single half-open probe.
	// Must be in (0, cbMaxOpenTimeout].
	OpenTimeout time.Duration
	// HalfOpenProbes is the maximum number of probe deliveries admitted while
	// half-open; one probe is sufficient to decide recovery in normal deployments.
	// Must be in [1, cbMaxHalfOpenProbes].
	HalfOpenProbes int
}

// DefaultCircuitBreakerSettings returns the default per-endpoint circuit-breaker
// thresholds (TripThreshold=5, OpenTimeout=60s, HalfOpenProbes=1). These equal
// the values formerly hard-coded as package constants and are used when
// [WithCircuitBreakerSettings] is not provided. Callers (bootstrap's
// [WithWebhookCircuitBreaker], dispatch.BuildConsumers) use this to populate the
// explicit settings parameter rather than accepting a zero value.
func DefaultCircuitBreakerSettings() CircuitBreakerSettings {
	return CircuitBreakerSettings{
		TripThreshold:  5,
		OpenTimeout:    defaultCBOpenTimeout,
		HalfOpenProbes: 1,
	}
}

// Validate returns a non-nil error if any field is outside its valid range
// (fail-fast; complies with runtime-api.md §Option 范式: "强依赖 option 必须
// fail-fast，不静默 noop"). Called by [WithCircuitBreakerSettings] at Dispatcher
// construction time and by bootstrap's [WithWebhookCircuitBreaker] at phase-drain
// time.
//
// Lower bounds (> 0) reject zero/negative values.
// Upper bounds (cbMax*) are the no-disable defense-in-depth: a value equivalent
// to "never trip" or "never recover" is treated as an invalid configuration.
func (s CircuitBreakerSettings) Validate() error {
	if s.TripThreshold <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook circuit breaker: TripThreshold must be > 0",
			errcode.WithDetails(errcode.PublicInt("got", s.TripThreshold)))
	}
	if s.TripThreshold > cbMaxTripThreshold {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook circuit breaker: TripThreshold exceeds maximum",
			errcode.WithDetails(
				errcode.PublicInt("got", s.TripThreshold),
				errcode.PublicInt("max", cbMaxTripThreshold),
			))
	}
	if s.OpenTimeout <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook circuit breaker: OpenTimeout must be > 0",
			errcode.WithDetails(errcode.PublicDuration("got", s.OpenTimeout)))
	}
	if s.OpenTimeout > cbMaxOpenTimeout {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook circuit breaker: OpenTimeout exceeds maximum",
			errcode.WithDetails(
				errcode.PublicDuration("got", s.OpenTimeout),
				errcode.PublicDuration("max", cbMaxOpenTimeout),
			))
	}
	if s.HalfOpenProbes <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook circuit breaker: HalfOpenProbes must be > 0",
			errcode.WithDetails(errcode.PublicInt("got", s.HalfOpenProbes)))
	}
	if s.HalfOpenProbes > cbMaxHalfOpenProbes {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook circuit breaker: HalfOpenProbes exceeds maximum",
			errcode.WithDetails(
				errcode.PublicInt("got", s.HalfOpenProbes),
				errcode.PublicInt("max", cbMaxHalfOpenProbes),
			))
	}
	return nil
}

// makeCircuitReadyToTrip returns a ReadyToTrip predicate that opens the breaker
// once consecutive failures exceed tripThreshold. A closure per circuitGate is
// acceptable (one gate per Dispatcher, not one per endpoint).
func makeCircuitReadyToTrip(tripThreshold int) func(circuitbreaker.Counts) bool {
	return func(c circuitbreaker.Counts) bool {
		return c.ConsecutiveFailures > uint32(tripThreshold) //nolint:gosec // tripThreshold is validated > 0
	}
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
	clk        clock.Clock
	settings   CircuitBreakerSettings
	newBreaker func(circuitbreaker.Config, clock.Clock) (*circuitbreaker.Breaker, error)
	mu         sync.Mutex
	breakers   map[string]*circuitbreaker.Breaker
}

// newCircuitGate builds an enabled circuit gate. clk is the dispatcher's clock,
// shared so open→half-open transitions advance with the same time source the
// tests drive. settings must have been validated before calling newCircuitGate.
func newCircuitGate(clk clock.Clock, settings CircuitBreakerSettings) *circuitGate {
	clock.MustHaveClock(clk, "webhook.newCircuitGate")
	return &circuitGate{
		clk:        clk,
		settings:   settings,
		newBreaker: circuitbreaker.New,
		breakers:   make(map[string]*circuitbreaker.Breaker),
	}
}

// Allow gates a delivery for key. It returns allowed=true and a done callback
// (call exactly once with the circuitProbeOutcome) when the (tenant, endpoint)
// circuit is closed or admits a half-open probe; allowed=false and a nil done
// when the circuit is open.
func (g *circuitGate) Allow(key circuitEndpointKey) (allowed bool, done func(err error)) {
	b, err := g.breakerFor(key)
	if b == nil {
		errText := "<nil>"
		if err != nil {
			errText = err.Error()
		}
		// Unreachable: breakerFor returns nil only if breaker construction
		// failed, which happens solely on an empty Name — and logName always
		// yields a non-empty "<host>#<fingerprint>". Kept as a defensive,
		// lint-required handling of New's error: fail open so a hypothetical
		// construction bug degrades to "no breaker protection", never to
		// "delivery blocked". Log at Error (correctness failure).
		slog.Error("webhook: circuit breaker construction failed, failing open",
			slog.String("tenant", key.tenant),
			slog.String("endpoint", key.logName()),
			slog.String("error", errText))
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
func (g *circuitGate) breakerFor(key circuitEndpointKey) (*circuitbreaker.Breaker, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rk := key.registryKey()
	if b, ok := g.breakers[rk]; ok {
		return b, nil
	}
	if len(g.breakers) >= circuitGateMaxEndpoints {
		for k := range g.breakers { // evict one arbitrary entry
			delete(g.breakers, k)
			break
		}
	}
	b, err := g.newBreaker(circuitbreaker.Config{
		Name:        key.logName(),                     // safe log label: host#fingerprint, no path/query
		MaxRequests: uint32(g.settings.HalfOpenProbes), //nolint:gosec // validated > 0
		Timeout:     g.settings.OpenTimeout,
		ReadyToTrip: makeCircuitReadyToTrip(g.settings.TripThreshold),
	}, g.clk)
	if err != nil {
		return nil, err
	}
	g.breakers[rk] = b
	return b, nil
}
