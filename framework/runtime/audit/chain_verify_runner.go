package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// chain_verify_runner.go — the transport-neutral orchestrator behind the #1755
// admin audit chain verify tool. It enumerates every per-(namespace, tenant)
// audit chain via a ledger.ChainVerifyStore (admin pool) and full-chain verifies
// each, producing an aggregate report. It is a system-integrity operation (sibling
// of VerifyBootstrapTailOnStartup): it returns only integrity verdicts, never audit
// row content, and carries no cross-tenant data-read obligation.
//
// Surfacing splits by cardinality (observability.md): per-chain detail (namespace,
// tenant_id, first-invalid seq) goes ONLY to the report body + slog; the metrics
// are AGGREGATE with a single frozen `outcome` label — tenant_id is unbounded and
// MUST NOT be a metric label.

// chainVerifyTimeout caps a whole VerifyAll run so a large chain fleet cannot stall
// the trigger indefinitely. Mirrors the 30s startup-verify budget
// (bootstrapTailVerifyStartupTimeout) for a consistent operator latency envelope.
const chainVerifyTimeout = 30 * time.Second

// run outcome label values — the closed set of the only metric label this tool
// emits (audit_chain_verify_runs_total{outcome}). The metric name + label KEY are
// frozen by TestChainVerifyMetrics_FrozenSet; these values are the sole producers
// of that label (see emit).
const (
	outcomeSuccess      = "success"
	outcomeInvalidFound = "invalid_found"
	outcomeError        = "error"
)

// ChainVerifyResult is one chain's integrity verdict. It carries ONLY verdict
// scalars + chain identity — NO audit row content (no payload / hash / actor) —
// so surfacing it never leaks cross-tenant audit data (the no-content guarantee is
// Hard by this field set; TestChainVerifyResult_FieldSetFrozen pins it).
type ChainVerifyResult struct {
	Namespace       string
	TenantID        string
	TailSeq         int64
	Valid           bool
	FirstInvalidSeq int64 // -1 when valid; the first invalid seq_no otherwise
	MissingGenesis  bool  // MinSeq != 1 — genesis rows absent (distinct from tamper)
	Err             error // non-nil → verify could not COMPLETE (infra/misconfig), distinct from a tamper (Valid=false, Err=nil)
}

// ChainVerifyReport aggregates one VerifyAll run. The trigger layer (HTTP handler /
// CLI) maps it to a typed wire/stdout shape — it is transport-neutral here.
type ChainVerifyReport struct {
	TotalChains   int
	InvalidChains int // tamper: Valid==false && Err==nil
	ErroredChains int // Err != nil (could not complete)
	Results       []ChainVerifyResult
	Duration      time.Duration
}

// AllValid reports whether every chain verified intact (no tamper, no error).
func (r ChainVerifyReport) AllValid() bool {
	return r.InvalidChains == 0 && r.ErroredChains == 0
}

// chainVerifyMetrics holds the pre-registered aggregate instruments. AGGREGATE
// only — the single label is the frozen `outcome` on runs_total; the gauges +
// histogram are label-free. tenant_id / namespace / first_invalid_seq never become
// labels.
type chainVerifyMetrics struct {
	runs          metrics.CounterVec   // audit_chain_verify_runs_total{outcome}
	invalidChains metrics.GaugeVec     // audit_chain_verify_invalid_chains (last run)
	erroredChains metrics.GaugeVec     // audit_chain_verify_errored_chains (last run)
	duration      metrics.HistogramVec // audit_chain_verify_duration_seconds
}

func newChainVerifyMetrics(mp metrics.Provider) (*chainVerifyMetrics, error) {
	runs, err := mp.CounterVec(metrics.CounterOpts{
		Name:       "audit_chain_verify_runs_total",
		Help:       "Total admin audit chain verify runs, by outcome (success|invalid_found|error).",
		LabelNames: []string{"outcome"},
	})
	if err != nil {
		return nil, err
	}
	invalid, err := mp.GaugeVec(metrics.GaugeOpts{
		Name: "audit_chain_verify_invalid_chains",
		Help: "Number of per-(namespace,tenant) audit chains found tampered in the last verify run.",
	})
	if err != nil {
		return nil, err
	}
	errored, err := mp.GaugeVec(metrics.GaugeOpts{
		Name: "audit_chain_verify_errored_chains",
		Help: "Number of audit chains whose verify could not complete in the last run.",
	})
	if err != nil {
		return nil, err
	}
	duration, err := mp.HistogramVec(metrics.HistogramOpts{
		Name:    "audit_chain_verify_duration_seconds",
		Help:    "Wall-clock duration of a full admin audit chain verify run.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
	})
	if err != nil {
		return nil, err
	}
	return &chainVerifyMetrics{runs: runs, invalidChains: invalid, erroredChains: errored, duration: duration}, nil
}

// ChainVerifier orchestrates a full per-(namespace, tenant) audit chain verify run.
type ChainVerifier struct {
	store   ledger.ChainVerifyStore
	metrics *chainVerifyMetrics
	clock   clock.Clock
	logger  *slog.Logger
}

// NewChainVerifier wires the orchestrator. store is required (the admin-pool-backed
// ledger.ChainVerifyStore). mp may be metrics.NopProvider{} when no backend is
// configured (the instruments then discard). clk is the framework clock (positional
// per convention). logger defaults to slog.Default() when nil.
func NewChainVerifier(store ledger.ChainVerifyStore, mp metrics.Provider, clk clock.Clock, logger *slog.Logger) (*ChainVerifier, error) {
	if validation.IsNilInterface(store) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewChainVerifier requires a non-nil ChainVerifyStore")
	}
	if validation.IsNilInterface(mp) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewChainVerifier requires a non-nil metrics.Provider (use metrics.NopProvider{} for none)")
	}
	clock.MustHaveClock(clk, "audit.NewChainVerifier")
	m, err := newChainVerifyMetrics(mp)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ChainVerifier{store: store, metrics: m, clock: clk, logger: logger}, nil
}

// VerifyAll enumerates every chain and verifies [1, MaxSeq] of each, aggregating a
// report. It NEVER aborts on a single chain's failure — every chain is attempted so
// the report is complete. It emits aggregate metrics + slog (Info on all-valid,
// Error per invalid/errored chain). The returned error is reserved for run-level
// infra failures (enumerate failed / deadline); a tampered chain is in the report
// body, not the error — the trigger layer decides the wire status.
func (v *ChainVerifier) VerifyAll(ctx context.Context) (ChainVerifyReport, error) {
	ctx, cancel := context.WithTimeout(ctx, chainVerifyTimeout)
	defer cancel()
	start := v.clock.Now()

	chains, err := v.store.EnumerateChains(ctx)
	if err != nil {
		v.metrics.runs.With(metrics.Labels{"outcome": outcomeError}).Inc(ctx)
		v.logger.ErrorContext(ctx, "audit chain verify: enumerate chains failed", slog.Any("error", err))
		return ChainVerifyReport{Duration: v.clock.Since(start)}, errcode.Wrap(errcode.KindInternal,
			errcode.ErrInternal, "audit chain verify: enumerate chains failed", err)
	}

	report := ChainVerifyReport{TotalChains: len(chains)}
	for _, c := range chains {
		res := v.verifyOne(ctx, c)
		report.Results = append(report.Results, res)
		switch {
		case res.Err != nil:
			report.ErroredChains++
		case !res.Valid:
			report.InvalidChains++
		}
	}
	report.Duration = v.clock.Since(start)
	v.emit(ctx, report)
	return report, nil
}

// verifyOne verifies a single chain [1, MaxSeq] and logs its verdict. MaxSeq==0
// (no rows) is treated as valid-nothing-to-verify; the PG enumerate never emits
// such a row, but the mem path / a future backend might.
func (v *ChainVerifier) verifyOne(ctx context.Context, c ledger.ChainRef) ChainVerifyResult {
	res := ChainVerifyResult{
		Namespace:       c.Namespace,
		TenantID:        c.TenantID,
		TailSeq:         c.MaxSeq,
		FirstInvalidSeq: -1,
		MissingGenesis:  c.MinSeq != 1,
	}
	if c.MaxSeq <= 0 {
		res.Valid = true
		return res
	}
	valid, firstInvalid, err := v.store.VerifyChain(ctx, c.Namespace, c.TenantID, 1, c.MaxSeq)
	res.Valid = valid
	res.FirstInvalidSeq = firstInvalid
	res.Err = err

	switch {
	case err != nil:
		v.logger.ErrorContext(ctx, "audit chain verify: chain verify could not complete",
			slog.String("namespace", c.Namespace), slog.String("tenant_id", c.TenantID),
			slog.Any("error", err))
	case !valid:
		v.logger.ErrorContext(ctx, "audit chain verify: chain integrity broken",
			slog.String("namespace", c.Namespace), slog.String("tenant_id", c.TenantID),
			slog.Int64("first_invalid_seq", firstInvalid), slog.Bool("missing_genesis", res.MissingGenesis))
	}
	return res
}

// emit records aggregate metrics + the all-valid Info log for one completed run.
// outcome precedence: any errored chain → error; else any tampered chain →
// invalid_found; else success.
func (v *ChainVerifier) emit(ctx context.Context, report ChainVerifyReport) {
	outcome := outcomeSuccess
	switch {
	case report.ErroredChains > 0:
		outcome = outcomeError
	case report.InvalidChains > 0:
		outcome = outcomeInvalidFound
	}
	v.metrics.runs.With(metrics.Labels{"outcome": outcome}).Inc(ctx)
	v.metrics.invalidChains.With(metrics.Labels{}).Set(ctx, float64(report.InvalidChains))
	v.metrics.erroredChains.With(metrics.Labels{}).Set(ctx, float64(report.ErroredChains))
	v.metrics.duration.With(metrics.Labels{}).Observe(ctx, report.Duration.Seconds())

	if report.AllValid() {
		v.logger.InfoContext(ctx, "audit chain verify: all chains valid",
			slog.Int("total_chains", report.TotalChains), slog.Duration("duration", report.Duration))
	}
}
