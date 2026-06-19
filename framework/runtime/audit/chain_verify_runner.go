package audit

import (
	"context"
	"errors"
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

// logMsgPrefix is the shared slog/errcode message prefix for all chain-verify
// log lines. Extracted per go-standards (string repeated ≥ 3 times → const).
const logMsgPrefix = "audit chain verify: "

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
	// UnverifiedChains counts chains that were enumerated but NEVER attempted because
	// the run was truncated (deadline / client cancel) before the loop reached them.
	// They are distinct from errored chains (which were attempted and failed) and are
	// NOT listed in Results — only counted, so a truncated run's wire body stays
	// bounded by the actual problems rather than ballooning to one entry per skip.
	UnverifiedChains int
	// TimedOut is true when the 30s deadline (not a client cancel) truncated the run.
	// The chains the run never reached are counted in UnverifiedChains.
	TimedOut bool
	Results  []ChainVerifyResult
	Duration time.Duration
}

// AllValid reports whether the run COMPLETED with every chain intact — no tamper, no
// error, and no chain left unverified by a truncated run.
func (r ChainVerifyReport) AllValid() bool {
	return r.InvalidChains == 0 && r.ErroredChains == 0 && r.UnverifiedChains == 0
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
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30},
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
			logMsgPrefix+"NewChainVerifier requires a non-nil ChainVerifyStore")
	}
	if validation.IsNilInterface(mp) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			logMsgPrefix+"NewChainVerifier requires a non-nil metrics.Provider (use metrics.NopProvider{} for none)")
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
		v.logger.ErrorContext(ctx, logMsgPrefix+"enumerate chains failed", slog.Any("error", err))
		return ChainVerifyReport{Duration: v.clock.Since(start)}, errcode.Wrap(errcode.KindInternal,
			errcode.ErrInternal, logMsgPrefix+"enumerate chains failed", err)
	}

	report := ChainVerifyReport{TotalChains: len(chains)}
	for _, c := range chains {
		// The 30s deadline (or a client cancel) is a real execution budget, not just a
		// post-hoc report flag: stop issuing VerifyChain queries the moment the ctx is
		// done. Continuing would fire already-canceled queries and emit a per-chain
		// Error log for every remaining chain — an I/O + log storm on a large fleet.
		// The chains not reached are tallied as UnverifiedChains below (distinct from
		// an errored chain, which was actually attempted).
		if ctx.Err() != nil {
			break
		}
		res := v.verifyOne(ctx, c)
		report.Results = append(report.Results, res)
		switch {
		case res.Err != nil:
			report.ErroredChains++
		case !res.Valid:
			report.InvalidChains++
		}
	}
	// Every enumerated chain not in Results was skipped by a truncated run.
	report.UnverifiedChains = report.TotalChains - len(report.Results)
	report.Duration = v.clock.Since(start)
	// TimedOut: the 30s deadline (not a client cancel) truncated the run. Both forms
	// of truncation leave chains in UnverifiedChains; TimedOut narrows it to a budget
	// exhaustion vs a caller-driven cancel.
	report.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
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
	res.Err = err

	switch {
	case err != nil:
		// Errored chain: set FirstInvalidSeq=-1 (the store's returned value is a
		// meaningless init value for errored chains, not a real first-invalid seq).
		res.FirstInvalidSeq = -1
		v.logger.ErrorContext(ctx, logMsgPrefix+"chain verify could not complete",
			slog.String("namespace", c.Namespace), slog.String("tenant_id", c.TenantID),
			slog.Any("error", err))
	case !valid:
		res.FirstInvalidSeq = firstInvalid
		v.logger.ErrorContext(ctx, logMsgPrefix+"chain integrity broken",
			slog.String("namespace", c.Namespace), slog.String("tenant_id", c.TenantID),
			slog.Int64("first_invalid_seq", firstInvalid), slog.Bool("missing_genesis", res.MissingGenesis))
	default:
		res.FirstInvalidSeq = firstInvalid // -1 for valid chains
	}
	return res
}

// emit records aggregate metrics and ALWAYS logs a run summary.
// outcome precedence: any errored OR unverified chain → error (the run could not
// complete); else any tampered chain → invalid_found; else success.
func (v *ChainVerifier) emit(ctx context.Context, report ChainVerifyReport) {
	outcome := outcomeSuccess
	switch {
	case report.ErroredChains > 0 || report.UnverifiedChains > 0:
		// An errored chain (attempted, failed) or an unverified chain (truncated run
		// never reached it) both mean the run did not complete cleanly → error.
		outcome = outcomeError
	case report.InvalidChains > 0:
		outcome = outcomeInvalidFound
	}
	v.metrics.runs.With(metrics.Labels{"outcome": outcome}).Inc(ctx)
	v.metrics.invalidChains.With(metrics.Labels{}).Set(ctx, float64(report.InvalidChains))
	v.metrics.erroredChains.With(metrics.Labels{}).Set(ctx, float64(report.ErroredChains))
	v.metrics.duration.With(metrics.Labels{}).Observe(ctx, report.Duration.Seconds())

	if report.AllValid() {
		v.logger.InfoContext(ctx, logMsgPrefix+"all chains valid",
			slog.Int("total_chains", report.TotalChains),
			slog.Duration("duration", report.Duration))
	} else {
		v.logger.WarnContext(ctx, logMsgPrefix+"completed with issues",
			slog.Int("total_chains", report.TotalChains),
			slog.Int("invalid_chains", report.InvalidChains),
			slog.Int("errored_chains", report.ErroredChains),
			slog.Int("unverified_chains", report.UnverifiedChains),
			slog.Bool("timed_out", report.TimedOut),
			slog.Duration("duration", report.Duration))
	}
}
