// Package decoypkg provides a homonym of runtime/bootstrap.WithManagedResource.
// The GREEN fixture calls it to prove the funnel's type-resolution discriminates
// by package path (Pkg().Path()) and does NOT over-fire on a same-named func from
// an unrelated package.
package decoypkg

// WithManagedResource is intentionally named identically to
// runtime/bootstrap.WithManagedResource but belongs to a different package.
func WithManagedResource(_ any) any { return nil }
