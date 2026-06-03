package webhook_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	rtwh "github.com/ghbvf/gocell/runtime/webhook"
)

// TestReceiver_RecordsSignatureFailureReason asserts ServeHTTP records
// webhook_signature_failures_total{source,reason} with the correctly classified
// reason for each verification-failure branch.
func TestReceiver_RecordsSignatureFailureReason(t *testing.T) {
	tests := []struct {
		name       string
		wantReason string
		// build returns the receiver under test and the request to drive it.
		build func(t *testing.T, m kwh.Metrics) (*rtwh.Receiver, *http.Request)
	}{
		{
			name:       "missing_header",
			wantReason: "missing_header",
			build: func(t *testing.T, m kwh.Metrics) (*rtwh.Receiver, *http.Request) {
				req := signedRequest(t, []byte(`{"k":"v"}`), "d1")
				req.Header.Del("X-Signature")
				return newMetricReceiver(t, testStore(t), fixedNow, m), req
			},
		},
		{
			name:       "invalid_header",
			wantReason: "invalid_header",
			build: func(t *testing.T, m kwh.Metrics) (*rtwh.Receiver, *http.Request) {
				req := signedRequest(t, []byte(`{"k":"v"}`), "d1")
				// Non-integer timestamp → verifier.validateTimestamp →
				// ErrWebhookInvalidHeader → ReasonInvalidHeader (runs before HMAC).
				req.Header.Set("X-Timestamp", "not-a-number")
				return newMetricReceiver(t, testStore(t), fixedNow, m), req
			},
		},
		{
			name:       "bad_signature",
			wantReason: "bad_signature",
			build: func(t *testing.T, m kwh.Metrics) (*rtwh.Receiver, *http.Request) {
				req := signedRequest(t, []byte(`{"k":"v"}`), "d1")
				req.Header.Set("X-Signature", "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
				return newMetricReceiver(t, testStore(t), fixedNow, m), req
			},
		},
		{
			name:       "unknown_source",
			wantReason: "unknown_source",
			build: func(t *testing.T, m kwh.Metrics) (*rtwh.Receiver, *http.Request) {
				req := signedRequest(t, []byte(`{"k":"v"}`), "d1")
				// Empty store → spec.SourceID lookup miss.
				return newMetricReceiver(t, kwh.NewSourceRegistry(), fixedNow, m), req
			},
		},
		{
			name:       "timestamp_expired",
			wantReason: "timestamp_expired",
			build: func(t *testing.T, m kwh.Metrics) (*rtwh.Receiver, *http.Request) {
				req := signedRequest(t, []byte(`{"k":"v"}`), "d1")
				// Verifier clock advanced beyond tolerance vs. the fixedNow signature.
				return newMetricReceiver(t, testStore(t), fixedNow.Add(testReplaySkew), m), req
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The Metrics instrument fields are sealed (unexported) — the only way
			// to build a recording Metrics is RegisterMetrics over a Provider
			// (WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 upstream seal). The test drives
			// the real constructor and reads back from the fake provider.
			p := newFakeProvider()
			m, err := kwh.RegisterMetrics(p)
			if err != nil {
				t.Fatalf("RegisterMetrics: %v", err)
			}
			recv, req := tc.build(t, m)

			recv.ServeHTTP(httptest.NewRecorder(), req)

			got := p.counterValue("webhook_signature_failures_total",
				kernelmetrics.Labels{"source": testSourceID, "reason": tc.wantReason})
			if got != 1 {
				t.Errorf("signature_failures{reason=%q} = %d, want 1 (recorded keys: %v)",
					tc.wantReason, got, p.counters["webhook_signature_failures_total"].keys())
			}
		})
	}
}

// TestReceiver_RecordsIdempotencyHit asserts a duplicate delivery (ClaimDone /
// ClaimBusy) records webhook_idempotency_hits_total{source}.
func TestReceiver_RecordsIdempotencyHit(t *testing.T) {
	for _, state := range []idempotency.ClaimState{idempotency.ClaimDone, idempotency.ClaimBusy} {
		p := newFakeProvider()
		m, err := kwh.RegisterMetrics(p)
		if err != nil {
			t.Fatalf("RegisterMetrics: %v", err)
		}
		clk := clockmock.New(fixedNow)
		verifier, err := kwh.NewHMACVerifier(clk, kwh.WithTolerance(testTolerance*time.Second))
		if err != nil {
			t.Fatalf("NewHMACVerifier: %v", err)
		}
		claimer := &fakeClaimer{results: []claimResult{{state: state}}}
		handler := func(_ context.Context, _ kwh.Delivery) error { return nil }
		recv, err := rtwh.NewReceiver(clk, testSpec(), verifier, testStore(t), claimer, handler, rtwh.WithMetrics(m))
		if err != nil {
			t.Fatalf("NewReceiver: %v", err)
		}

		recv.ServeHTTP(httptest.NewRecorder(), signedRequest(t, []byte(`{"k":"v"}`), "d1"))

		if got := p.counterValue("webhook_idempotency_hits_total", kernelmetrics.Labels{"source": testSourceID}); got != 1 {
			t.Errorf("state %v: idempotency_hits{source} = %d, want 1", state, got)
		}
	}
}

// newMetricReceiver builds a Receiver whose verifier clock is verifierNow (to
// drive the timestamp-expiry branch) and injects metrics m.
func newMetricReceiver(t *testing.T, store kwh.SourceStore, verifierNow time.Time, m kwh.Metrics) *rtwh.Receiver {
	t.Helper()
	clk := clockmock.New(verifierNow)
	verifier, err := kwh.NewHMACVerifier(clk, kwh.WithTolerance(testTolerance*time.Second))
	if err != nil {
		t.Fatalf("NewHMACVerifier: %v", err)
	}
	handler := func(_ context.Context, _ kwh.Delivery) error { return nil }
	recv, err := rtwh.NewReceiver(clk, testSpec(), verifier, store,
		idempotency.NewInMemClaimer(clk), handler, rtwh.WithMetrics(m))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return recv
}

// --- fakeCounterVec: records With(labels).Inc calls by label tuple. ---

type fakeCounterVec struct {
	obs map[string]int
}

func (v *fakeCounterVec) Registered() bool { return true }
func (v *fakeCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	return &fakeCounter{vec: v, key: labelKey(l)}
}

func (v *fakeCounterVec) value(l kernelmetrics.Labels) int { return v.obs[labelKey(l)] }
func (v *fakeCounterVec) keys() []string {
	out := make([]string, 0, len(v.obs))
	for k := range v.obs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type fakeCounter struct {
	vec *fakeCounterVec
	key string
}

func (c *fakeCounter) Inc(_ context.Context)            { c.vec.obs[c.key]++ }
func (c *fakeCounter) Add(_ context.Context, n float64) { c.vec.obs[c.key] += int(n) }

func labelKey(l kernelmetrics.Labels) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	key := ""
	for _, k := range keys {
		key += k + "=" + l[k] + ";"
	}
	return key
}

var _ kernelmetrics.CounterVec = (*fakeCounterVec)(nil)

// --- fakeProvider: minimal recording Provider for the sealed-Metrics path. ---
//
// The kwh.Metrics instrument fields are unexported (WEBHOOK-METRIC-LABEL-VALUES-
// FROZEN-01 upstream seal), so external tests can no longer inject a fake vec via
// a struct literal. They must build a Metrics through the real RegisterMetrics
// constructor over a Provider; fakeProvider returns recording fakeCounterVecs
// keyed by metric name and leaves histograms to the embedded NopProvider.
type fakeProvider struct {
	kernelmetrics.NopProvider
	counters map[string]*fakeCounterVec
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{counters: map[string]*fakeCounterVec{}}
}

func (p *fakeProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	v := &fakeCounterVec{obs: map[string]int{}}
	p.counters[opts.Name] = v
	return v, nil
}

// counterValue returns the recorded count for name+labels, or -1 if the metric
// was never registered.
func (p *fakeProvider) counterValue(name string, l kernelmetrics.Labels) int {
	if v, ok := p.counters[name]; ok {
		return v.value(l)
	}
	return -1
}

var _ kernelmetrics.Provider = (*fakeProvider)(nil)
