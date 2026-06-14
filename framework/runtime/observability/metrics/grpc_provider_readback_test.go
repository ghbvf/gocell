package metrics_test

import (
	"context"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics/metricstest"
)

// TestGRPCProviderCollector_LabelReadback drives metrics.NewGRPCProviderCollector
// through the shared spyProvider and reads back the label tuples threaded to the
// underlying counter + histogram instruments. After the cell parameter changed
// from a raw string to the sealed CellLabel, the only provider-path coverage was
// "registers and records without panic" — which would still pass even if
// RecordRPC dropped or mislabeled cell/method/code. This proves the labels
// actually reach BOTH instruments and that a distinct code keeps a distinct
// series (the readback conformance the HTTP Collector gets from
// metricstest.RunCollectorConformance, which the gRPC provider path lacked).
func TestGRPCProviderCollector_LabelReadback(t *testing.T) {
	spy := newSpyProvider()
	c, err := metrics.NewGRPCProviderCollector(spy, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("NewGRPCProviderCollector: %v", err)
	}

	ctx := context.Background()
	c.RecordRPC(ctx, metricstest.Label("accesscore"), "/pkg.Svc/Do", "OK", 0.01)
	c.RecordRPC(ctx, metricstest.Label("accesscore"), "/pkg.Svc/Do", "OK", 0.02)
	c.RecordRPC(ctx, metricstest.RuntimeLabel(), "/pkg.Svc/Do", "Internal", 0.03)

	reqOps := spy.counterOps["grpc_server_requests_total"]
	if len(reqOps) != 3 {
		t.Fatalf("counter ops = %d, want 3 (one per RecordRPC)", len(reqOps))
	}
	assertGRPCLabels(t, "requests[0]", reqOps[0].labels, "accesscore", "/pkg.Svc/Do", "OK")
	assertGRPCLabels(t, "requests[2]", reqOps[2].labels, "_runtime", "/pkg.Svc/Do", "Internal")

	durOps := spy.histogramOps["grpc_server_request_duration_seconds"]
	if len(durOps) != 3 {
		t.Fatalf("histogram ops = %d, want 3 (one per RecordRPC)", len(durOps))
	}
	assertGRPCLabels(t, "duration[0]", durOps[0].labels, "accesscore", "/pkg.Svc/Do", "OK")
	if got := durOps[0].value; got < 0.0099 || got > 0.0101 {
		t.Errorf("duration[0] value = %v, want ~0.01 (duration must thread to the histogram)", got)
	}

	// Label isolation: the Internal-code op must not collapse onto the OK series.
	if got := reqOps[2].labels["code"]; got != "Internal" {
		t.Errorf("requests[2] code = %q, want Internal (distinct code must keep a distinct series)", got)
	}
}

// assertGRPCLabels checks the cell/method/code label tuple on one recorded op.
func assertGRPCLabels(t *testing.T, what string, got kernelmetrics.Labels, cell, method, code string) {
	t.Helper()
	if got["cell"] != cell {
		t.Errorf("%s cell label = %q, want %q", what, got["cell"], cell)
	}
	if got["method"] != method {
		t.Errorf("%s method label = %q, want %q", what, got["method"], method)
	}
	if got["code"] != code {
		t.Errorf("%s code label = %q, want %q", what, got["code"], code)
	}
}
