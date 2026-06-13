package syshealth

import "context"

// healthViewKey is an unexported context key so no external package can inject a
// HealthView under it through any path other than WithHealthView. Mirrors the
// auth.authorizerKey sealed-construction funnel (AUTHORIZER-CTX-FUNNEL-01).
//
// INVARIANT: SYSHEALTH-VIEW-CTX-FUNNEL-01
// Upstream: WithHealthView is the SOLE injector. Downstream: HealthViewFromContext
// is the SOLE reader.
//
// AI-robust Grade (funnel — upstream + downstream stated separately):
//   - WRITE seal = Hard: the unexported key type makes any out-of-package write
//     under healthViewKey a compile error (sealed construction + single holder).
//   - CALLSITE breadth = Medium: which production files may CALL the exported
//     WithHealthView / HealthViewFromContext is not expressible in the type
//     system (both must stay exported so bootstrap and healthread can call them
//     across packages); it is enforced by the SYSHEALTH-VIEW-CTX-FUNNEL-01
//     archtest (tools/archtest/syshealth_view_funnel_test.go) — writer
//     allowlisted to runtime/bootstrap, reader to corecells/syscore healthread.
//     The reader side has NO Hard backstop; that scan is its only guard. The
//     Hard-ization path (route-group-scoped injection / sealed injector token) is
//     backlog-tracked.
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
