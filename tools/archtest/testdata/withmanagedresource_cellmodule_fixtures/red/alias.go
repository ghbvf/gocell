package red

// import-alias form: bs.WithManagedResource(...) — the alias collapses to the
// same *types.Func under Uses, so the alias does not disguise the reference.
import bs "github.com/ghbvf/gocell/runtime/bootstrap"

var _ = bs.WithManagedResource(nil)
