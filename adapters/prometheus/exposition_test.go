package prometheus_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	gcprom "github.com/ghbvf/gocell/adapters/prometheus"
	"github.com/ghbvf/gocell/kernel/outbox"
	runtimemetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

// Integration-style tests that drive runtime / kernel collectors through
// the Prometheus adapter exactly as production does, then scrape the
// registry via the Prometheus text-exposition encoder. This is the
// missing regression layer the reviewer flagged (F4): earlier tests used
// Nop / spy / OTel ManualReader providers and never exercised the real
// Prometheus path — so the PR's "family name + bucket + dashboard
// compatible" promise was structurally unchecked.

func newProvider(t *testing.T) (*gcprom.MetricProvider, *prom.Registry) {
	t.Helper()
	reg := prom.NewRegistry()
	p, err := gcprom.NewMetricProvider(gcprom.MetricProviderConfig{
		Registry:  reg,
		Namespace: "gocell",
	})
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}
	return p, reg
}

// expose renders the registry in the Prometheus text exposition format.
// The output shape mirrors what /metrics would return, so assertions can
// grep the same byte stream the scraper sees.
func expose(t *testing.T, reg *prom.Registry) string {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, f := range fams {
		if err := enc.Encode(f); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	return buf.String()
}

// TestPrometheusExposition_HTTPCollector_FamiliesAndLabels asserts that
// routing runtime/observability/metrics.NewProviderCollector through the
// Prometheus adapter emits the pre-migration metric family names
// (gocell_http_requests_total / gocell_http_request_duration_seconds),
// the expected label set (method / route / status / cell), and — for the
// histogram — the DefaultDurationBuckets boundaries. Any breakage here
// is an immediate Grafana regression.
func TestPrometheusExposition_HTTPCollector_FamiliesAndLabels(t *testing.T) {
	p, reg := newProvider(t)
	c, err := runtimemetrics.NewProviderCollector(p, runtimemetrics.ProviderCollectorConfig{
		CellID: "test-cell",
	})
	if err != nil {
		t.Fatalf("NewProviderCollector: %v", err)
	}
	c.RecordRequest("GET", "/api/v1/users", 200, 0.15)
	c.RecordRequest("POST", "/api/v1/users", 201, 0.05)

	body := expose(t, reg)

	// Family names preserved (exact match pre/post Provider migration).
	for _, want := range []string{
		"# TYPE gocell_http_requests_total counter",
		"# TYPE gocell_http_request_duration_seconds histogram",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q; got:\n%s", want, body)
		}
	}
	// Label arity + expected values on the counter.
	if !strings.Contains(body, `gocell_http_requests_total{cell="test-cell",method="GET",route="/api/v1/users",status="200"}`) {
		t.Errorf("counter sample missing for GET 200 route; got:\n%s", body)
	}
	if !strings.Contains(body, `gocell_http_requests_total{cell="test-cell",method="POST",route="/api/v1/users",status="201"}`) {
		t.Errorf("counter sample missing for POST 201 route; got:\n%s", body)
	}
	// DefaultDurationBuckets still present on the histogram — the first
	// bucket boundary is 0.005 and the second is 0.01.
	if !strings.Contains(body, `le="0.005"`) || !strings.Contains(body, `le="0.01"`) {
		t.Errorf("histogram missing default bucket boundaries 0.005 / 0.01; got:\n%s", body)
	}
}

// TestPrometheusExposition_RelayCollector_FamiliesAndBuckets pins the
// outbox relay side of the same contract. The relay's six metric names
// (relayed / poll_duration / batch_size / reclaimed / cleaned) were lifted
// verbatim out of the deleted adapters/prometheus/relay_collector.go;
// dashboards and alert routes depend on them being identical.
func TestPrometheusExposition_RelayCollector_FamiliesAndBuckets(t *testing.T) {
	p, reg := newProvider(t)
	c, err := outbox.NewProviderRelayCollector(p, "test-cell")
	if err != nil {
		t.Fatalf("NewProviderRelayCollector: %v", err)
	}

	c.RecordPollCycle(outbox.PollCycleResult{
		Published: 2, Retried: 0, Dead: 1, Skipped: 3,
		ClaimDur: 10 * time.Millisecond, PublishDur: 50 * time.Millisecond, WriteBackDur: 5 * time.Millisecond,
	})
	c.RecordBatchSize(6)
	c.RecordReclaim(4)
	c.RecordCleanup(12, 2)

	body := expose(t, reg)

	for _, want := range []string{
		"# TYPE gocell_outbox_relayed_total counter",
		"# TYPE gocell_outbox_poll_duration_seconds histogram",
		"# TYPE gocell_outbox_batch_size histogram",
		"# TYPE gocell_outbox_reclaimed_total counter",
		"# TYPE gocell_outbox_cleaned_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q; got:\n%s", want, body)
		}
	}
	// Non-zero outcomes labelled correctly (skipped zero suppression is a
	// collector rule inherited from the old adapter).
	for _, want := range []string{
		`gocell_outbox_relayed_total{cell="test-cell",outcome="published"} 2`,
		`gocell_outbox_relayed_total{cell="test-cell",outcome="dead"} 1`,
		`gocell_outbox_relayed_total{cell="test-cell",outcome="skipped"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing sample %q; got:\n%s", want, body)
		}
	}
	// Retried was zero on the RecordPollCycle call; the collector skips
	// zero-valued outcomes to keep cardinality clean — pin this rule.
	if strings.Contains(body, `outcome="retried"`) {
		t.Errorf("exposition must not carry retried outcome when count was zero; got:\n%s", body)
	}
	// DefaultRelayPollBuckets start at 0.005 and include 2.5.
	if !strings.Contains(body, `le="0.005"`) || !strings.Contains(body, `le="2.5"`) {
		t.Errorf("poll_duration histogram missing default buckets; got:\n%s", body)
	}
	// DefaultRelayBatchBuckets include 500 as the upper boundary.
	if !strings.Contains(body, `le="500"`) {
		t.Errorf("batch_size histogram missing 500 default boundary; got:\n%s", body)
	}
}
