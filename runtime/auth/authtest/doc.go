// Package authtest provides test-only auth Policy helpers for runtime
// middleware behavior tests. Production code (cells/, examples/) must NOT
// import this package; archtest enforces the boundary.
//
// For injecting a test Principal in cell handler tests, use
// auth.TestContext(subject, roles) instead. RequireAuthenticated is
// for testing the middleware layer itself (i.e., what happens when no
// Principal is present at all); cell handler tests should use
// auth.AnyRole(...) + auth.TestContext(...) to exercise RBAC paths.
package authtest
