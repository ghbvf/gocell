// Package authtest provides test-only auth Policy helpers for runtime
// middleware behavior tests.
//
// Location: this package lives under runtime/internal/, so the Go compiler
// itself refuses imports from outside the runtime/ subtree (cells/, examples/,
// kernel/, cmd/, adapters/, tools/, tests/). Before issue #638 this was a
// top-level runtime/auth/authtest package whose boundary was enforced only
// by archtest AUTH-AUTHTEST-B (string-anchor scan). The internal/ move
// upgrades B from archtest-Medium to compiler-Hard (see ai-robust.md
// §Hard 范本目录 → "internal/ wrap 包"). AUTH-AUTHTEST-C (the "only _test.go
// may import" rule) remains as archtest because Go has no built-in test-file
// import constraint.
//
// Distinct from kernel/auth/authtest: there is a second authtest package at
// kernel/auth/authtest exporting AuthPlan Must* helpers (MustAuthJWT,
// MustAuthJWTFromAssembly, MustAuthServiceToken) used by composition-root
// test wiring. Its consumer set spans cmd/+runtime/+kernel/+tests/, so no
// single internal/ placement covers it; AUTH-AUTHTEST-C governs the test-file
// boundary for both packages uniformly. Issue #638 only addresses this
// package (runtime/internal/authtest); kernel/auth/authtest is out of scope.
//
// For injecting a test Principal in cell handler tests, use
// auth.TestContext(subject, roles) instead. RequireAuthenticated is
// for testing the middleware layer itself (i.e., what happens when no
// Principal is present at all); cell handler tests should use
// auth.AnyRole(...) + auth.TestContext(...) to exercise RBAC paths.
package authtest
