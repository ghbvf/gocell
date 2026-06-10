//go:build archtest

// invariants:
//   - INVARIANT: GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01 (RED fixture coverage)
package archtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGRPCMetricsLabelCellIDCtxSource01_RedFixtureDetected asserts the
// production scan (scanGRPCMetricsLabelPkg) catches the unrelated-identifier
// form that a non-provenance rule would silently accept (blind spot B2). The
// fixture at tools/archtest/internal/grpcmetricsfixture/grpc_metrics_red.go
// (gated `//go:build archtest_fixture`) calls metrics.ResolveCellLabel and
// RecordRPC, but passes a bogus UNRELATED CellLabel identifier as the cell
// label. Every structural assertion therefore passes; only the one
// cellLabelFromResolve provenance assertion fires, so the scan must yield
// exactly 1 diagnostic.
//
// # Why a RED fixture is required
//
// Without it, the production rule's zero-diagnostic outcome on real UnaryMetrics
// carries no information: the object-identity provenance binding could be
// silently broken AND the production code happens to comply. The fixture is a
// known-positive sample, so an accidental regression of cellLabelFromResolve
// turns this test red.
func TestGRPCMetricsLabelCellIDCtxSource01_RedFixtureDetected(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t,
		Fixture(FixtureOpts{Tests: false},
			[]string{"./tools/archtest/internal/grpcmetricsfixture/..."}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() || !strings.HasSuffix(p.Pkg.Path(), "/grpcmetricsfixture") {
				return nil
			}
			return scanGRPCMetricsLabelPkg(p)
		},
	)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	// Exact equality (not ≥ 1): the fixture is intentionally minimal so that
	// every non-provenance assertion passes and ONLY the object-identity
	// provenance check fires. If the fixture changes intentionally, update the
	// expected count.
	assert.Len(t, diags, 1,
		"GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01 must yield exactly 1 hit on the "+
			"unrelated-ident fixture (the metrics.ResolveCellLabel provenance binding)")

	// Strong specificity: the one diagnostic must be the provenance assertion,
	// not some other assertion regressing.
	var sawResolveProvenance bool
	for _, d := range diags {
		if strings.Contains(d.Message, "assigned from "+"metrics.ResolveCellLabel") {
			sawResolveProvenance = true
		}
	}
	assert.True(t, sawResolveProvenance,
		"the rule must flag that the bogus arg is not the variable assigned from metrics.ResolveCellLabel")
}
