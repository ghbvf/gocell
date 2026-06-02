//go:build archtest_fixture

// Package grpcmetricsfixture is a RED fixture for
// GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01 (see
// tools/archtest/grpc_metrics_label_test.go). It is gated by the
// archtest_fixture build tag and loaded only by
// TestGRPCMetricsLabelCellIDCtxSource01_RedFixtureDetected via Run(t, Fixture(...));
// it is never part of a production build.
package grpcmetricsfixture

import (
	"context"
	"time"

	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// UnaryMetrics deliberately mimics the shape of
// runtime/grpc/interceptor.UnaryMetrics — it reads kernel/ctxkeys.CellIDFrom and
// metrics.RuntimeCellSentinel and calls GRPCCollector.RecordRPC — but feeds an
// UNRELATED identifier (bogus) as the cell label instead of the ctx-derived
// cellID. This is exactly the blind spot B2 that the pre-provenance rule
// accepted: every structural assertion (reads ctx, uses sentinel, calls
// RecordRPC, arg is an ident and not a literal, ctx before record, sentinel
// before record) passes, so the scan must fire ONLY the two cellIDProvenance
// diagnostics — the bogus var is the object referenced by neither the sentinel
// init nor the CellIDFrom branch assignment.
func UnaryMetrics(collector metrics.GRPCCollector) func(context.Context, string) {
	return func(ctx context.Context, method string) {
		cellID := metrics.RuntimeCellSentinel
		if v, ok := kernelctxkeys.CellIDFrom(ctx); ok && v != "" {
			cellID = v
		}
		_ = cellID                   // correctly-attributed value, intentionally unused
		bogus := "constructor-value" // unrelated ident — NOT the ctx-derived cellID
		collector.RecordRPC(ctx, bogus, method, "OK", time.Second.Seconds())
	}
}
