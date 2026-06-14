package testwait

import "time"

// DeterministicWithinForTest forwards to the unexported deterministicWithin so
// the white-box self-test can exercise the safety-net-expiry branch with a
// short budget. It lives in export_test.go, so it is compiled ONLY into
// testwait's own test binary — business test code in other packages cannot
// call it, and the timeout-free public Deterministic funnel is unaffected.
func DeterministicWithinForTest[T any](t TB, signal <-chan T, budget time.Duration, msgAndArgs ...any) T {
	return deterministicWithin(t, signal, budget, msgAndArgs...)
}
