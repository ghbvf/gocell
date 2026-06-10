package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ref: go-kratos/kratos middleware/metrics/metrics.go — nil-guard fast-path pattern.
// Adopted: counter+histogram with label-based classification.
// Deviated: auth-specific reason labels (expired/invalid_kid/wrong_alg) vs generic code labels.

// AuthMetrics holds pre-registered metric instruments for auth operations.
type AuthMetrics struct {
	tokenVerifyTotal    metrics.CounterVec
	tokenVerifyDuration metrics.HistogramVec
	serviceVerifyTotal  metrics.CounterVec
}

// AccountLockoutMetrics is the standalone observability instrument for the
// auto-lockout transitions emitted by corecells/accesscore/internal/accountlockout.
// It registers a single CounterVec (`auth_account_lockout_total`) and lives
// apart from AuthMetrics so the cell composition root can wire it
// independently of bootstrap's router-level auth metrics — the two use the
// same metrics.Provider but register disjoint metric names, avoiding the
// "already registered" error that would result from constructing AuthMetrics
// twice with the same provider.
type AccountLockoutMetrics struct {
	total metrics.CounterVec
}

// NewAuthMetrics registers auth metric instruments with the given provider.
func NewAuthMetrics(p metrics.Provider) (*AuthMetrics, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "auth: metrics provider must not be nil")
	}

	tvt, err := p.CounterVec(metrics.CounterOpts{
		Name:       "auth_token_verify_total",
		Help:       "Total number of JWT token verifications.",
		LabelNames: []string{"result", "reason"},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: register auth_token_verify_total: %w", err)
	}

	tvd, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "auth_token_verify_duration_seconds",
		Help:       "Duration of JWT token verification in seconds.",
		LabelNames: []string{"result"},
		Buckets:    []float64{.0001, .0005, .001, .005, .01, .025, .05, .1},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: register auth_token_verify_duration_seconds: %w", err)
	}

	svt, err := p.CounterVec(metrics.CounterOpts{
		Name:       "auth_service_token_verify_total",
		Help:       "Total number of service token verifications.",
		LabelNames: []string{"result", "reason"},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: register auth_service_token_verify_total: %w", err)
	}

	return &AuthMetrics{
		tokenVerifyTotal:    tvt,
		tokenVerifyDuration: tvd,
		serviceVerifyTotal:  svt,
	}, nil
}

// NewAccountLockoutMetrics registers the `auth_account_lockout_total`
// CounterVec for ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01. The composition
// root wires this into AccessCore via WithLockoutMetrics; the cell hands the
// returned recorder to accountlockout.NewService.
//
// reason label ∈ {"threshold_locked", "lazy_unlocked"}.
// "threshold_locked" — consecutive-failure threshold reached → user auto-locked.
// "lazy_unlocked" — locked_until elapsed; sessionlogin transparently
// reactivated the account inside the login tx (silent on the outbox,
// visible only via this counter + slog per ADR §"lazy-unlock 审计策略").
func NewAccountLockoutMetrics(p metrics.Provider) (*AccountLockoutMetrics, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"auth: metrics provider must not be nil")
	}
	c, err := p.CounterVec(metrics.CounterOpts{
		Name:       "auth_account_lockout_total",
		Help:       "Total number of account auto-lockout transitions, by reason (threshold_locked / lazy_unlocked).",
		LabelNames: []string{"reason"},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: register auth_account_lockout_total: %w", err)
	}
	return &AccountLockoutMetrics{total: c}, nil
}

// IncAccountLockout implements accountlockout.MetricsRecorder. reason MUST be
// one of {"threshold_locked", "lazy_unlocked"}; arbitrary strings are
// recorded as-is, but the accountlockout package only emits the two values
// listed.
func (m *AccountLockoutMetrics) IncAccountLockout(ctx context.Context, reason string) {
	if m == nil {
		return
	}
	m.total.With(metrics.Labels{"reason": reason}).Inc(ctx)
}

func (m *AuthMetrics) recordTokenVerify(ctx context.Context, result, reason string, duration time.Duration) {
	if m == nil {
		return
	}
	m.tokenVerifyTotal.With(metrics.Labels{"result": result, "reason": reason}).Inc(ctx)
	m.tokenVerifyDuration.With(metrics.Labels{"result": result}).Observe(ctx, duration.Seconds())
}

// recordTokenVerifyCounter increments the token verify counter without recording
// a duration. Used for early-exit paths (e.g. missing token) where no
// verification work was performed and a 0 duration would pollute the histogram.
func (m *AuthMetrics) recordTokenVerifyCounter(ctx context.Context, result, reason string) {
	if m == nil {
		return
	}
	m.tokenVerifyTotal.With(metrics.Labels{"result": result, "reason": reason}).Inc(ctx)
}

func (m *AuthMetrics) recordServiceVerify(ctx context.Context, result, reason string) {
	if m == nil {
		return
	}
	m.serviceVerifyTotal.With(metrics.Labels{"result": result, "reason": reason}).Inc(ctx)
}

// classifyTokenError maps a token verification error to a short reason label.
//
// The KindUnavailable / CategoryInfra branch must precede every token-side
// branch: verifier-side infrastructure failures (JWKS down, KMS unreachable —
// see jwt.go:hasExplicitInfraSignal) carry their own *errcode.Error and must
// surface as a distinct "service_unavailable" reason so SLO dashboards and
// alerts can separate "credential failures" from "auth dependency degraded".
// Without this branch the 503 path collapsed into "invalid_token" and made
// the failure mode invisible (Finding #3 PR #490 second review).
func classifyTokenError(err error) string {
	if err == nil {
		return "ok"
	}
	var ec *errcode.Error
	if errors.As(err, &ec) {
		if ec.Kind == errcode.KindUnavailable || ec.Category == errcode.CategoryInfra {
			return "service_unavailable"
		}
		if ec.Code == errcode.ErrAuthInvalidTokenIntent {
			return "invalid_intent"
		}
	}
	switch {
	case errors.Is(err, jwt.ErrTokenExpired) || errors.Is(err, jwt.ErrTokenNotValidYet):
		return "expired"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return "invalid_signature"
	default:
		// Covers invalid kid, wrong signing method, malformed tokens.
		// jwt.ErrTokenMalformed, jwt.ErrTokenUnverifiable, and custom keyFunc errors.
		msg := err.Error()
		switch {
		case strings.Contains(msg, "kid"):
			return "invalid_kid"
		case strings.Contains(msg, "signing method"):
			return "wrong_alg"
		default:
			return "invalid_token"
		}
	}
}
