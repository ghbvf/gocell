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
			sig := &fakeCounterVec{obs: map[string]int{}}
			m := kwh.Metrics{SignatureFailures: sig}
			recv, req := tc.build(t, m)

			recv.ServeHTTP(httptest.NewRecorder(), req)

			got := sig.value(kernelmetrics.Labels{"source": testSourceID, "reason": tc.wantReason})
			if got != 1 {
				t.Errorf("signature_failures{reason=%q} = %d, want 1 (recorded keys: %v)",
					tc.wantReason, got, sig.keys())
			}
		})
	}
}

// TestReceiver_RecordsIdempotencyHit asserts a duplicate delivery (ClaimDone /
// ClaimBusy) records webhook_idempotency_hits_total{source}.
func TestReceiver_RecordsIdempotencyHit(t *testing.T) {
	for _, state := range []idempotency.ClaimState{idempotency.ClaimDone, idempotency.ClaimBusy} {
		idem := &fakeCounterVec{obs: map[string]int{}}
		m := kwh.Metrics{IdempotencyHits: idem}
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

		if got := idem.value(kernelmetrics.Labels{"source": testSourceID}); got != 1 {
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
