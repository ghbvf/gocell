package bootstrap

// export_test.go — white-box test helpers exported only during test compilation.
// Follows the Go convention: file name ends in _test.go; package is the
// non-test package (package bootstrap, not package bootstrap_test) so we can
// access unexported identifiers.

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/go-chi/chi/v5"
)

// ApplyPolicyForTest applies the policy's middleware to mux if p implements the
// internal mountablePolicy interface. External test packages cannot call
// mountablePolicy.Apply directly; this helper bridges the gap.
//
// If p does not implement mountablePolicy (e.g., a pure cell.Policy from
// outside runtime/bootstrap), ApplyPolicyForTest is a no-op.
func ApplyPolicyForTest(p cell.Policy, mux *chi.Mux) {
	if mp, ok := p.(mountablePolicy); ok {
		mp.Apply(mux)
	}
}
