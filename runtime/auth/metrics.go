package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// AuthMetrics holds pre-registered metric instruments for auth operations.
type AuthMetrics struct {
	tokenVerifyTotal    metrics.CounterVec
	tokenVerifyDuration metrics.HistogramVec
	serviceVerifyTotal  metrics.CounterVec
}

// NewAuthMetrics registers auth metric instruments with the given provider.
func NewAuthMetrics(p metrics.Provider) (*AuthMetrics, error) {
	if p == nil {
		return nil, errcode.New(errcode.ErrValidationFailed, "auth: metrics provider must not be nil")
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

func (m *AuthMetrics) recordTokenVerify(result, reason string, duration time.Duration) {
	if m == nil {
		return
	}
	m.tokenVerifyTotal.With(metrics.Labels{"result": result, "reason": reason}).Inc()
	m.tokenVerifyDuration.With(metrics.Labels{"result": result}).Observe(duration.Seconds())
}

func (m *AuthMetrics) recordServiceVerify(result, reason string) {
	if m == nil {
		return
	}
	m.serviceVerifyTotal.With(metrics.Labels{"result": result, "reason": reason}).Inc()
}

// classifyTokenError maps a token verification error to a short reason label.
func classifyTokenError(err error) string {
	if err == nil {
		return "ok"
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
