// Package green is a GREEN reverse-self-check fixture for
// WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01. It references a same-named func from a
// DIFFERENT package; the funnel's type-resolution must NOT flag it (Pkg().Path()
// is decoypkg, not runtime/bootstrap).
package green

import decoy "github.com/ghbvf/gocell/tools/archtest/testdata/withmanagedresource_cellmodule_fixtures/decoypkg"

var _ = decoy.WithManagedResource(nil)
