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

	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// UnaryMetrics deliberately mimics the shape of
// runtime/grpc/interceptor.UnaryMetrics — it resolves the cell label through
// metrics.ResolveCellLabel and calls GRPCCollector.RecordRPC — but feeds an
// UNRELATED CellLabel identifier (bogus zero value) as the cell label instead of
// the funnel-resolved cell. This is exactly the blind spot B2 that a
// non-provenance rule would accept: every structural assertion (calls
// ResolveCellLabel, calls RecordRPC, arg is an ident and not a literal, no inline
// CellIDFrom, resolve before record) passes, so the scan must fire ONLY the one
// cellLabelFromResolve diagnostic — the bogus var is the object referenced by no
// metrics.ResolveCellLabel assignment.
func UnaryMetrics(collector metrics.GRPCCollector) func(context.Context, string) {
	return func(ctx context.Context, method string) {
		cell := metrics.ResolveCellLabel(ctx, nil)
		_ = cell                    // correctly-resolved label, intentionally unused
		var bogus metrics.CellLabel // zero value, NOT from the funnel for this call
		collector.RecordRPC(ctx, bogus, method, "OK", time.Second.Seconds())
	}
}
