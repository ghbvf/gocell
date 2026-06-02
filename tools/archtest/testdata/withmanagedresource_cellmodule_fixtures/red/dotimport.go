package red

// dot-import form: a bare WithManagedResource(...) ident with no package
// qualifier — a SelectorExpr-based scan misses it entirely, but Uses[ident]
// resolves the bare ident to runtime/bootstrap's func.
import . "github.com/ghbvf/gocell/runtime/bootstrap"

var _ = WithManagedResource(nil)
