package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/require"
)

// TestDispatcher_Handle_RecordsResultMetric asserts Handle records
// webhook_deliveries_total{result,source} with the status-aware result label
// for each delivery-outcome class, and a duration sample when an HTTP attempt
// was made.
func TestDispatcher_Handle_RecordsResultMetric(t *testing.T) {
	tests := []struct {
		name        string
		status      int    // HTTP status the fake target returns (0 = use SSRF target)
		ssrfTarget  string // when non-empty, use this target with a no-loopback policy
		wantResult  string
		wantHTTPDur bool // whether a delivery-duration sample is expected
	}{
		{name: "2xx_success", status: http.StatusOK, wantResult: "success", wantHTTPDur: true},
		{name: "4xx_client_error", status: http.StatusNotFound, wantResult: "client_error", wantHTTPDur: true},
		{name: "5xx_server_error", status: http.StatusInternalServerError, wantResult: "server_error", wantHTTPDur: true},
		{name: "ssrf_blocked", ssrfTarget: "http://169.254.169.254/latest/meta-data/", wantResult: "blocked", wantHTTPDur: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newRecordingProvider()
			m, err := RegisterMetrics(p)
			require.NoError(t, err)

			var target string
			var policy *SafePolicy
			if tc.ssrfTarget != "" {
				target = tc.ssrfTarget
				policy = NewSafePolicy() // no loopback → link-local target blocked
			} else {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
				}))
				defer srv.Close()
				target = srv.URL
				policy = NewSafePolicy(WithAllowLoopback())
			}

			d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
				dispatchTestSigner(t), policy, staticSelector(target),
				WithMetrics(m, "stripe"))
			require.NoError(t, err)

			d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))

			got := p.counterValue("webhook_deliveries_total",
				kernelmetrics.Labels{"result": tc.wantResult, "source": "stripe"})
			require.Equal(t, int64(1), got, "deliveries_total{result=%q} should be 1", tc.wantResult)

			durCount := p.histogramCount("webhook_delivery_duration_seconds",
				kernelmetrics.Labels{"source": "stripe"})
			if tc.wantHTTPDur {
				require.Equal(t, int64(1), durCount, "expected one delivery-duration sample")
			} else {
				require.Equal(t, int64(0), durCount, "prepare-failure must not record a delivery-duration sample")
			}
		})
	}
}

// TestDispatcher_Handle_TransportError_RecordsTransportResult asserts a transport
// fault (timeout, no HTTP response) records result=transport_error and a
// duration sample (the time spent before the failure).
func TestDispatcher_Handle_TransportError_RecordsTransportResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)

	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL),
		WithMetrics(m, "stripe"), WithDeliveryTimeout(10*time.Millisecond))
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	require.Equal(t, outbox.DispositionRequeue, res.Disposition)

	got := p.counterValue("webhook_deliveries_total",
		kernelmetrics.Labels{"result": "transport_error", "source": "stripe"})
	require.Equal(t, int64(1), got)
	require.Equal(t, int64(1), p.histogramCount("webhook_delivery_duration_seconds",
		kernelmetrics.Labels{"source": "stripe"}))
}

// TestDispatcher_Handle_NoMetrics_NoPanic asserts a dispatcher constructed
// without WithMetrics (zero recorder) records as a no-op.
func TestDispatcher_Handle_NoMetrics_NoPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
	require.NoError(t, err)
	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
	require.Equal(t, outbox.DispositionAck, res.Disposition)
}
