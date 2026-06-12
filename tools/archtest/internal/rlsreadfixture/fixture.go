//go:build archtest_fixture

// Package rlsreadfixture is an archtest RED fixture for TENANT-RLS-READ-CALLER-01.
//
// The production rule resolves references to accesscore's
// ports.UserRepository / ports.RoleRepository RLS-table READ methods via
// go/types (info.Uses → *types.Func, receiver-bound) and asserts each sits in a
// caller allowlist. Those interfaces live in corecells/accesscore/internal/ports
// — an internal package tools/archtest cannot import — so this fixture declares
// its OWN stand-in UserRepository interface, and the fixture test runs the SAME
// detector core targeted at THIS package. It proves the detector actually fires
// on a genuine read reference (resolution + receiver-binding + read-method-set),
// and that a WRITE reference does NOT trip it. A 0 result means the detector
// regressed and a new unscoped RLS read could slip in unnoticed.
//
// (Build-tag gated behind archtest_fixture so it stays out of normal / Production
// builds and is loaded only by the Fixture() RunScope.)
package rlsreadfixture

import "context"

// UserRepository is a local stand-in mirroring the shape of accesscore's
// ports.UserRepository: a READ method (in the detector's read-method set) and a
// WRITE method (excluded). The detector keys on the receiver interface name plus
// the method name, so this local interface exercises the same resolution path.
type UserRepository interface {
	GetByIDInTenant(ctx context.Context, t, id string) (any, error) // READ — must flag
	Create(ctx context.Context, t string, u any) error              // WRITE — must NOT flag
}

// badUnscopedRead references the read method from this non-allowlisted file. The
// detector MUST report this reference.
func badUnscopedRead(ctx context.Context, r UserRepository) {
	_, _ = r.GetByIDInTenant(ctx, "t", "id")
}

// writeOutsideScope references a write method; the read-method-set filter MUST
// exclude it (no diagnostic), proving the rule does not over-match writes.
func writeOutsideScope(ctx context.Context, r UserRepository) {
	_ = r.Create(ctx, "t", nil)
}
