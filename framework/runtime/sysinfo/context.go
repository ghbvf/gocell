package sysinfo

import "context"

// systemViewKey is unexported so callers can only inject SystemView through
// WithSystemView. This mirrors runtime/syshealth's request-context funnel.
//
// INVARIANT: SYSINFO-VIEW-CTX-FUNNEL-01
// Production callers of WithSystemView/SystemViewFromContext are locked by
// tools/archtest/sysinfo_view_funnel_test.go: bootstrap is the sole writer and
// syscore systemread is the sole reader. _test.go files may round-trip the
// funnel directly.
type systemViewKey struct{}

// WithSystemView injects the runtime SystemView into the request context.
func WithSystemView(ctx context.Context, v SystemView) context.Context {
	return context.WithValue(ctx, systemViewKey{}, v)
}

// SystemViewFromContext retrieves the SystemView injected by WithSystemView.
func SystemViewFromContext(ctx context.Context) (SystemView, bool) {
	v := ctx.Value(systemViewKey{})
	if v == nil {
		return nil, false
	}
	sv, ok := v.(SystemView)
	if !ok || sv == nil {
		return nil, false
	}
	return sv, true
}
