package syshealth

import "context"

// healthViewKey is an unexported context key so no external package can inject a
// HealthView through any path other than WithHealthView. Mirrors the
// auth.authorizerKey sealed-construction funnel (AUTHORIZER-CTX-FUNNEL-01).
//
// AI-robust Grade: Hard — "sealed construction" + "single sanctioned holder".
//
// INVARIANT: SYSHEALTH-VIEW-CTX-FUNNEL-01
// Upstream: WithHealthView is the SOLE injector. Downstream: HealthViewFromContext
// is the SOLE reader. The unexported key type makes any out-of-package write a
// compile error.
type healthViewKey struct{}

// WithHealthView injects the runtime HealthView into the context. It is the sole
// upstream funnel entry; the composition root (bootstrap) calls it once when
// building the primary listener request-context chain. Business code reads via
// HealthViewFromContext, never writes.
func WithHealthView(ctx context.Context, v HealthView) context.Context {
	return context.WithValue(ctx, healthViewKey{}, v)
}

// HealthViewFromContext retrieves the HealthView injected by WithHealthView.
// Returns (nil, false) when absent — the syscore handler treats absence as
// fail-closed (503), never as an empty-but-OK report.
func HealthViewFromContext(ctx context.Context) (HealthView, bool) {
	v := ctx.Value(healthViewKey{})
	if v == nil {
		return nil, false
	}
	hv, ok := v.(HealthView)
	if !ok || hv == nil {
		return nil, false
	}
	return hv, true
}
