package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
)

// ref: go-kratos/kratos middleware/metrics/metrics.go — counter+histogram by-label
// classification + nil-guard fast path (same pattern as AuthMetrics).

// pdpDecisionLabel is the closed value-set for the `decision` label of
// auth_pdp_decision_total / auth_pdp_decision_duration_seconds. It is a defined
// string type so a bare string is not assignable without a conversion — the only
// constant values that can reach the metric are the three consts below. The set is
// frozen by AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01 (A1 golden + A2 callsite guard).
type pdpDecisionLabel string

const (
	// pdpDecisionAllow — the PDP returned a permit.
	pdpDecisionAllow pdpDecisionLabel = "allow"
	// pdpDecisionDeny — the PDP returned a (policy) deny.
	pdpDecisionDeny pdpDecisionLabel = "deny"
	// pdpDecisionError — Authorize returned an error (policy store unavailable /
	// tenant missing → 503). Kept distinct from a policy "deny" (403) so on-call
	// can separate infra failure from authorization failure (#2027 F12).
	pdpDecisionError pdpDecisionLabel = "error"
)

// PDPMetrics holds the pre-registered instruments for PDP (authorization-decision)
// observability. It is wired by the composition root (bootstrap) and consumed via
// observableAuthorizer; a nil *PDPMetrics is a no-op (metrics are best-effort and
// must never affect the verdict).
type PDPMetrics struct {
	decisionTotal    metrics.CounterVec
	decisionDuration metrics.HistogramVec
}

// NewPDPMetrics registers the PDP decision instruments with the given provider.
// The `action` label is the sealed authz.Permission spelling (closed-set by the
// Permission registry, so no separate freeze is needed); the `decision` label is
// the frozen pdpDecisionLabel set.
func NewPDPMetrics(p metrics.Provider) (*PDPMetrics, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "auth: metrics provider must not be nil")
	}
	total, err := p.CounterVec(metrics.CounterOpts{
		Name:       "auth_pdp_decision_total",
		Help:       "Total number of PDP (ABAC) authorization decisions, by action and decision (allow/deny/error).",
		LabelNames: []string{"action", "decision"},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: register auth_pdp_decision_total: %w", err)
	}
	// Histogram is labeled by `decision` only (not `action`): latency is dominated
	// by the policy-store load + evaluate, which is per-tenant not per-action, and
	// keeping it off the histogram bounds bucket cardinality (the counter already
	// carries `action` for per-action deny-rate analysis). Buckets reach 2.5s so a
	// slow PG policy load / cold start does not collapse every sample into +Inf and
	// break p95 (mirrors runtime/auth/refresh bucket ceiling).
	dur, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "auth_pdp_decision_duration_seconds",
		Help:       "Duration of a PDP authorization decision (policy-store load + evaluate) in seconds, by decision.",
		LabelNames: []string{"decision"},
		Buckets:    []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: register auth_pdp_decision_duration_seconds: %w", err)
	}
	return &PDPMetrics{decisionTotal: total, decisionDuration: dur}, nil
}

// recordDecision records one PDP decision. The decision argument is the typed
// pdpDecisionLabel funnel — a bare string cannot reach this method, and the
// AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01 callsite guard bans inline conversions.
// A nil receiver is a no-op (fail-open: missing metrics never deny a request).
func (m *PDPMetrics) recordDecision(ctx context.Context, action string, decision pdpDecisionLabel, dur time.Duration) {
	if m == nil {
		return
	}
	m.decisionTotal.With(metrics.Labels{"action": action, "decision": string(decision)}).Inc(ctx)
	m.decisionDuration.With(metrics.Labels{"decision": string(decision)}).Observe(ctx, dur.Seconds())
}

// Compile-time check: observableAuthorizer implements Authorizer.
var _ Authorizer = (*observableAuthorizer)(nil)

// observableAuthorizer decorates an Authorizer with PDP decision metrics. It is
// the elegant kernel of the issue's "refactor" tier (#2027): the metric lives in
// one composition-root decorator so the hot paths (enforcePermission and the PDP
// Service.Authorize) stay free of instrumentation. It measures the full
// inner.Authorize latency (policy-store load + evaluate) and classifies the PURE
// PDP decision (gate-level obligation handling in enforcePermission is downstream).
//
// ref: go-kratos/kratos middleware/metrics — interceptor-style metric decorator.
type observableAuthorizer struct {
	inner Authorizer
	m     *PDPMetrics
	clk   clock.Clock
}

// NewObservableAuthorizer wraps inner with decision metrics. clk is the mandatory
// positional clock (no time.Now in the hot path; go-standards). m may be nil — the
// decorator then becomes a transparent pass-through (recordDecision no-ops),
// matching the bootstrap fail-open path when no metrics provider is configured.
func NewObservableAuthorizer(clk clock.Clock, inner Authorizer, m *PDPMetrics) Authorizer {
	clock.MustHaveClock(clk, "auth.NewObservableAuthorizer")
	// inner is a mandatory dependency — fail-fast at composition (like the clock
	// above) rather than nil-deref on the first request (go-standards / runtime-api.md
	// 强依赖 fail-fast). m is intentionally optional (nil → no-op recorder).
	if validation.IsNilInterface(inner) {
		panic(panicregister.Approved("auth-observable-authorizer-nil-inner",
			errcode.Assertion("auth.NewObservableAuthorizer: inner Authorizer must not be nil")))
	}
	return &observableAuthorizer{inner: inner, m: m, clk: clk}
}

// Authorize records the classified decision + the inner call latency, then returns
// the inner verdict unchanged. Metrics are recorded after the call and never alter
// the returned (Decision, error).
func (o *observableAuthorizer) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	start := o.clk.Now()
	dec, err := o.inner.Authorize(ctx, subject, resource, action)
	o.m.recordDecision(ctx, action, classifyDecision(dec, err), o.clk.Since(start))
	return dec, err
}

// classifyDecision maps an Authorize outcome to the frozen pdpDecisionLabel: an
// error (store down / tenant missing) → error; an allow → allow; else deny.
func classifyDecision(dec authz.Decision, err error) pdpDecisionLabel {
	switch {
	case err != nil:
		return pdpDecisionError
	case dec.IsAllow():
		return pdpDecisionAllow
	default:
		return pdpDecisionDeny
	}
}
