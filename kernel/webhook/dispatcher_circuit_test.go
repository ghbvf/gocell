package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// countingServer is an httptest server whose handler counts hits and returns a
// status code read from a swappable atomic, so a test can flip an endpoint
// healthy↔unhealthy mid-run while observing whether the dispatcher actually
// reached it (fast-fail vs real POST).
func countingServer(t *testing.T, initialStatus int32) (srv *httptest.Server, hits *atomic.Int32, status *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	status = &atomic.Int32{}
	status.Store(initialStatus)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(srv.Close)
	return srv, hits, status
}

// deliverN runs Handle n times against the same payload.
func deliverN(t *testing.T, d *Dispatcher, n int) {
	t.Helper()
	for range n {
		d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	}
}

// requireCircuitOpen asserts a HandleResult is the fast-fail Requeue carrying
// the ErrCircuitOpen errcode (no HTTP attempt was made).
func requireCircuitOpen(t *testing.T, res outbox.HandleResult) {
	t.Helper()
	require.Equal(t, outbox.DispositionRequeue, res.Disposition, "circuit-open fast-fail must Requeue")
	var ee *errcode.Error
	require.True(t, errors.As(res.Err, &ee), "circuit-open result must carry *errcode.Error, got %T", res.Err)
	assert.Equal(t, errcode.ErrCircuitOpen, ee.Code, "circuit-open result must carry ErrCircuitOpen")
}

// TestDispatcher_Handle_CircuitOpensAndFastFails verifies that after
// circuitTripThreshold+1 consecutive 5xx failures the endpoint circuit opens and
// the next delivery fast-fails (Requeue + ErrCircuitOpen) WITHOUT a real HTTP
// POST — the whole point of the breaker.
func TestDispatcher_Handle_CircuitOpensAndFastFails(t *testing.T) {
	srv, hits, _ := countingServer(t, http.StatusInternalServerError)

	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL),
		WithMetrics(m, "stripe"))
	require.NoError(t, err)

	// Trip: every 5xx counts as an endpoint-health failure. After
	// circuitTripThreshold+1 consecutive failures the breaker opens.
	tripCount := circuitTripThreshold + 1
	deliverN(t, d, tripCount)
	require.Equal(t, tripCount, int(hits.Load()), "all trip deliveries reach the endpoint while closed")

	// Next delivery: circuit is open → fast-fail, no HTTP attempt.
	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	requireCircuitOpen(t, res)
	assert.Equal(t, tripCount, int(hits.Load()), "open circuit must NOT POST to the endpoint")

	got := p.counterValue("webhook_deliveries_total",
		kernelmetrics.Labels{"result": string(deliveryCircuitOpen), "source": "stripe"})
	assert.Equal(t, int64(1), got, "circuit-open fast-fail must record result=circuit_open")

	// Fast-fail must NOT record a duration sample: no HTTP attempt was made.
	// Only the tripCount real POSTs should have contributed duration samples.
	// Access the histogram directly (labelKey is package-internal) to avoid
	// adding a second call to histogramCount with the same name — which would
	// trigger the unparam linter since there is only one histogram metric.
	durKey := labelKey([]string{labelSource}, kernelmetrics.Labels{labelSource: "stripe"})
	var durationCount int64
	if h, ok := p.histograms[metricWebhookDeliveryDuration]; ok {
		durationCount = h.obs[durKey]
	}
	assert.Equal(t, int64(tripCount), durationCount,
		"only real HTTP deliveries must record a duration sample; fast-fail must not")
}

// TestDispatcher_Handle_CircuitHalfOpenRecoversOnSuccess verifies that after the
// open timeout elapses the breaker admits a single probe, and a 2xx probe closes
// the circuit so normal delivery resumes.
func TestDispatcher_Handle_CircuitHalfOpenRecoversOnSuccess(t *testing.T) {
	srv, hits, status := countingServer(t, http.StatusInternalServerError)

	fc := clockmock.New(time.Unix(dispatchTestTS, 0))
	d, err := NewDispatcher(fc, dispatchTestSigner(t),
		NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
	require.NoError(t, err)

	// Trip the circuit.
	deliverN(t, d, circuitTripThreshold+1)
	requireCircuitOpen(t, d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`))))
	hitsAfterTrip := hits.Load()

	// Endpoint recovers; advance past the open timeout → half-open probe admitted.
	status.Store(http.StatusOK)
	fc.Advance(circuitOpenTimeout + time.Second)

	probe := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	assert.Equal(t, outbox.DispositionAck, probe.Disposition, "successful half-open probe acks")
	assert.Equal(t, hitsAfterTrip+1, hits.Load(), "half-open admits exactly one probe POST")

	// Circuit closed: subsequent deliveries flow normally.
	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	assert.Equal(t, outbox.DispositionAck, res.Disposition, "circuit closed after successful probe")
	assert.Equal(t, hitsAfterTrip+2, hits.Load())
}

// TestDispatcher_Handle_CircuitHalfOpenReopensOnFailure verifies that a failed
// half-open probe reopens the circuit and the next delivery fast-fails again.
func TestDispatcher_Handle_CircuitHalfOpenReopensOnFailure(t *testing.T) {
	srv, hits, _ := countingServer(t, http.StatusInternalServerError)

	fc := clockmock.New(time.Unix(dispatchTestTS, 0))
	d, err := NewDispatcher(fc, dispatchTestSigner(t),
		NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
	require.NoError(t, err)

	deliverN(t, d, circuitTripThreshold+1)
	requireCircuitOpen(t, d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`))))

	// Advance to half-open; probe still fails (server stays 5xx) → reopen.
	fc.Advance(circuitOpenTimeout + time.Second)
	probe := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	assert.Equal(t, outbox.DispositionRequeue, probe.Disposition, "failed probe still Requeues the delivery")
	hitsAfterProbe := hits.Load()

	// Circuit reopened immediately → next delivery fast-fails, no POST.
	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	requireCircuitOpen(t, res)
	assert.Equal(t, hitsAfterProbe, hits.Load(), "reopened circuit must not POST")
}

// TestDispatcher_Handle_Circuit4xxDoesNotTrip verifies the breaker tracks
// endpoint HEALTH, not payload validity: a steady stream of 4xx (endpoint
// reachable, rejecting the payload) never trips the circuit.
func TestDispatcher_Handle_Circuit4xxDoesNotTrip(t *testing.T) {
	srv, hits, _ := countingServer(t, http.StatusNotFound)

	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
	require.NoError(t, err)

	const n = 10 // well past circuitTripThreshold
	for range n {
		res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
		require.Equal(t, outbox.DispositionRequeue, res.Disposition, "4xx is Requeue (standard-webhooks)")
	}
	assert.Equal(t, n, int(hits.Load()), "every 4xx delivery must reach the endpoint — circuit stays closed")
}

// TestDispatcher_Handle_CircuitSSRFNotCounted verifies a dial-time SSRF block
// (a config/policy issue, not an unhealthy endpoint) does NOT count toward the
// breaker: many SSRF rejects in a row never open the circuit, so the disposition
// stays Reject (DLX) rather than degrading to a circuit-open Requeue.
func TestDispatcher_Handle_CircuitSSRFNotCounted(t *testing.T) {
	privateResolver := fakeResolver{addrs: []net.IPAddr{{IP: net.ParseIP("10.0.0.5")}}}
	policy := NewSafePolicy(withResolver(privateResolver))
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), policy, staticSelector("http://blocked.example.test/"))
	require.NoError(t, err)

	const n = 10 // well past circuitTripThreshold
	for range n {
		res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
		require.Equal(t, outbox.DispositionReject, res.Disposition,
			"dial-time SSRF block is a permanent Reject and must not be counted by the breaker")
	}

	// If SSRF had counted as a failure the circuit would now be open and the
	// next call would fast-fail with ErrCircuitOpen; instead it stays Reject.
	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	require.Equal(t, outbox.DispositionReject, res.Disposition)
	var ee *errcode.Error
	require.True(t, errors.As(res.Err, &ee))
	assert.Equal(t, errcode.ErrWebhookSSRFBlocked, ee.Code, "circuit must not have opened from SSRF blocks")
}

// TestDispatcher_Handle_CircuitPerEndpointIsolation verifies each target URL has
// its own breaker: tripping endpoint A does not fast-fail deliveries to the
// healthy endpoint B.
func TestDispatcher_Handle_CircuitPerEndpointIsolation(t *testing.T) {
	srvA, hitsA, _ := countingServer(t, http.StatusInternalServerError)
	srvB, hitsB, _ := countingServer(t, http.StatusOK)

	sel := func(_ context.Context, payload []byte) (string, error) {
		if string(payload) == "a" {
			return srvA.URL, nil
		}
		return srvB.URL, nil
	}
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), sel)
	require.NoError(t, err)

	// Trip endpoint A.
	for range circuitTripThreshold + 1 {
		d.Handle(context.Background(), newTestEntry(t, []byte("a")))
	}
	requireCircuitOpen(t, d.Handle(context.Background(), newTestEntry(t, []byte("a"))))

	// Endpoint B is independent and healthy.
	resB := d.Handle(context.Background(), newTestEntry(t, []byte("b")))
	assert.Equal(t, outbox.DispositionAck, resB.Disposition, "endpoint B circuit unaffected by A")
	assert.Equal(t, 1, int(hitsB.Load()), "endpoint B was reached")
	_ = hitsA
}

// TestDispatcher_Handle_Circuit429Trips verifies that 429 responses count as
// endpoint-health failures and trip the circuit after circuitTripThreshold+1
// consecutive responses.
func TestDispatcher_Handle_Circuit429Trips(t *testing.T) {
	srv, hits, _ := countingServer(t, http.StatusTooManyRequests)

	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
	require.NoError(t, err)

	// Trip: 429 counts as an endpoint-health failure. Use a named const rather
	// than deliverN to avoid an unparam lint hit on deliverN's n parameter.
	const tripCount = circuitTripThreshold + 1
	for range tripCount {
		d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	}
	require.Equal(t, tripCount, int(hits.Load()), "all trip deliveries reach the endpoint while closed")

	// Next delivery: circuit is open → fast-fail.
	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	requireCircuitOpen(t, res)
	assert.Equal(t, tripCount, int(hits.Load()), "open circuit must NOT POST to the endpoint")
}

// TestCircuitGate_FailOpenOnEmptyEndpoint verifies that Allow("") — which
// yields an empty host → breaker construction fails → nil breaker — fails open:
// allowed=true and the done callback does not panic.
func TestCircuitGate_FailOpenOnEmptyEndpoint(t *testing.T) {
	g := newCircuitGate(clockmock.New(time.Unix(0, 0)))
	// An empty endpoint: url.Parse("") gives host=""; breakerFor returns nil.
	allow, done := g.Allow("")
	assert.True(t, allow, "nil breaker must fail open (allow=true)")
	require.NotNil(t, done, "fail-open must return a non-nil done callback")
	// done must not panic regardless of the error passed.
	assert.NotPanics(t, func() { done(nil) })
}

// TestDispatcher_Handle_CircuitTransportFaultTrips verifies that repeated
// transport faults (connection refused after server close) count as endpoint-
// health failures and trip the circuit after circuitTripThreshold+1 attempts.
func TestDispatcher_Handle_CircuitTransportFaultTrips(t *testing.T) {
	// Start a server, capture its URL, then close it so every dial attempt
	// after closure is refused — a genuine transport fault (not SSRF).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	target := srv.URL
	srv.Close() // close immediately; subsequent dials will be refused

	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(target))
	require.NoError(t, err)

	// Each delivery fails with a transport fault (connection refused) → Requeue.
	tripCount := circuitTripThreshold + 1
	for range tripCount {
		res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
		assert.Equal(t, outbox.DispositionRequeue, res.Disposition,
			"transport fault before trip must Requeue")
	}

	// Circuit is now open → next delivery fast-fails.
	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	requireCircuitOpen(t, res)
}

// TestCircuitGate_BoundedEviction verifies the per-endpoint breaker registry is
// bounded (DoS guard) — registering far more than the cap never grows the map
// past circuitGateMaxEndpoints.
func TestCircuitGate_BoundedEviction(t *testing.T) {
	g := newCircuitGate(clockmock.New(time.Unix(0, 0)))
	for i := range circuitGateMaxEndpoints + 100 {
		allow, done := g.Allow(fmt.Sprintf("http://e%d.example.test/", i))
		require.True(t, allow, "fresh endpoint breaker starts closed")
		done(nil)
	}
	assert.LessOrEqual(t, g.size(), circuitGateMaxEndpoints,
		"breaker registry must stay bounded at circuitGateMaxEndpoints")
}
