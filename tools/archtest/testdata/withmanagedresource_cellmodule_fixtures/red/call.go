// Package red is a RED reverse-self-check fixture for
// WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01. It references
// runtime/bootstrap.WithManagedResource in every form a cell module might use to
// bypass a CallExpr-only scan. The funnel's Uses-sweep detector must flag each.
//
// This file covers the CALL form and the FUNC-VALUE form (the latter is the
// reference a CallExpr.Fun-only walk would miss).
package red

import bootstrap "github.com/ghbvf/gocell/runtime/bootstrap"

// call form: bootstrap.WithManagedResource(...) in CallExpr.Fun position.
var _ = bootstrap.WithManagedResource(nil)

// func-value form: the reference is NOT in CallExpr.Fun position — a
// CallExpr-only walk misses it, but Uses[ident] still resolves to the func.
var _ = bootstrap.WithManagedResource
