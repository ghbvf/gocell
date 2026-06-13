package webhook

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/circuitbreaker"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

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

// circuitGate is the per-endpoint circuit-breaker registry a Dispatcher gates
// outbound delivery on. Each distinct target URL gets its own Breaker so one
// unhealthy receiver does not fast-fail deliveries to healthy ones. The map is
// bounded (circuitGateMaxEndpoints) as a DoS guard.
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

// Allow gates a delivery to endpoint. It returns allowed=true and a done
// callback (call exactly once with the circuitProbeOutcome) when the endpoint's
// circuit is closed or admits a half-open probe; allowed=false and a nil done
// when the circuit is open.
func (g *circuitGate) Allow(endpoint string) (allowed bool, done func(err error)) {
	b := g.breakerFor(endpoint)
	if b == nil {
		// Unreachable in practice: breakerFor only returns nil if breaker
		// construction failed, which happens solely on an empty Name — and
		// endpoint is a validated non-empty target URL. Fail open (allow, no-op
		// done) so a hypothetical construction bug degrades to "no breaker
		// protection", never to "delivery blocked".
		return true, func(error) {}
	}
	return b.Allow()
}

// size reports the number of tracked endpoints (test seam for the bounded-
// registry invariant).
func (g *circuitGate) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.breakers)
}

// breakerFor returns the breaker for endpoint, lazily constructing one. When the
// registry is at capacity it evicts an arbitrary entry first (DoS safety valve):
// at circuitGateMaxEndpoints distinct endpoints the victim choice is not
// correctness-critical — a re-observed endpoint simply rebuilds its breaker, the
// worst case being one lost open-state that re-trips on the next failure burst.
func (g *circuitGate) breakerFor(endpoint string) *circuitbreaker.Breaker {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b, ok := g.breakers[endpoint]; ok {
		return b
	}
	if len(g.breakers) >= circuitGateMaxEndpoints {
		for k := range g.breakers { // evict one arbitrary entry
			delete(g.breakers, k)
			break
		}
	}
	// New only errors on an empty Name; endpoint is a validated non-empty target
	// URL, so this construction cannot fail here. A nil return drives Allow's
	// documented fail-open path rather than a silent swallow.
	b, err := circuitbreaker.New(circuitbreaker.Config{
		Name:        endpoint,
		MaxRequests: circuitHalfOpenProbes,
		Timeout:     circuitOpenTimeout,
		ReadyToTrip: circuitReadyToTrip,
	}, g.clk)
	if err != nil {
		return nil
	}
	g.breakers[endpoint] = b
	return b
}
