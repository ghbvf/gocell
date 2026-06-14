package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/framework/runtime/syshealth"
)

// TestHealthViewInjector pins #1860: the primary-listener middleware injects the
// runtime HealthView into request context so the syscore handler can read it via
// syshealth.HealthViewFromContext. nil args are fine here — Report is never
// called; the injector only threads the view value through ctx.
func TestHealthViewInjector(t *testing.T) {
	view := syshealth.New(nil, nil)

	var got syshealth.HealthView
	var ok bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok = syshealth.HealthViewFromContext(r.Context())
	})

	healthViewInjector(view)(next).ServeHTTP(
		httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/admin/health/cells", nil))

	if !ok {
		t.Fatal("HealthView not present in context after injector ran")
	}
	if got != view {
		t.Fatal("injected HealthView identity mismatch")
	}
}

// TestHealthViewInjector_AbsentByDefault pins the fail-closed precondition: a
// request that never passed through the injector has no HealthView (the syscore
// handler then returns 503).
func TestHealthViewInjector_AbsentByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/health/cells", nil)
	if _, ok := syshealth.HealthViewFromContext(req.Context()); ok {
		t.Fatal("HealthView must be absent when the injector did not run (fail-closed)")
	}
}
