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
// form that the pre-provenance rule silently accepted (blind spot B2). The
// fixture at tools/archtest/internal/grpcmetricsfixture/grpc_metrics_red.go
// (gated `//go:build archtest_fixture`) reads ctxkeys.CellIDFrom +
// RuntimeCellSentinel and calls RecordRPC, but passes a bogus identifier as the
// cell label. Every structural assertion therefore passes; only the two
// cellIDProvenance assertions fire, so the scan must yield exactly 2
// diagnostics.
//
// # Why a RED fixture is required
//
// Without it, the production rule's zero-diagnostic outcome on real UnaryMetrics
// carries no information: the object-identity provenance binding could be
// silently broken AND the production code happens to comply. The fixture is a
// known-positive sample, so an accidental regression of cellIDProvenance turns
// this test red.
func TestGRPCMetricsLabelCellIDCtxSource01_RedFixtureDetected(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := RunTypedFixture(t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/grpcmetricsfixture/..."},
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

	// Exact equality (not ≥ 2): the fixture is intentionally minimal so that
	// every non-provenance assertion passes and ONLY the two object-identity
	// provenance checks fire. If the fixture changes intentionally, update the
	// expected count.
	assert.Len(t, diags, 2,
		"GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01 must yield exactly 2 hits on the "+
			"unrelated-ident fixture (the RuntimeCellSentinel-init + CellIDFrom-branch "+
			"provenance bindings)")

	// Strong specificity: the two diagnostics must be the provenance pair, not
	// some other assertion regressing. The sentinel-provenance message is the
	// only emitted one containing "RuntimeCellSentinel" (the usesSentinel
	// assertion passes, so its message is not emitted); the ctx-provenance
	// message is the only one containing "CellIDFrom(ctx) branch".
	var sawSentinelProvenance, sawCtxProvenance bool
	for _, d := range diags {
		if strings.Contains(d.Message, "RuntimeCellSentinel") {
			sawSentinelProvenance = true
		}
		if strings.Contains(d.Message, "CellIDFrom(ctx) branch") {
			sawCtxProvenance = true
		}
	}
	assert.True(t, sawSentinelProvenance,
		"the rule must flag that the bogus arg is not the variable initialized from RuntimeCellSentinel")
	assert.True(t, sawCtxProvenance,
		"the rule must flag that the bogus arg is not the variable assigned from the CellIDFrom(ctx) branch")
}
