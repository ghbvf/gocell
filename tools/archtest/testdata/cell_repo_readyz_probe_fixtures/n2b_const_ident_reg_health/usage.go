// Package n2b_const_ident_reg_health exercises the N2b RED fixture for
// CELL-REPO-READYZ-PROBE-01: a direct reg.Health call whose first argument is a
// package-level const string identifier rather than a *ast.BasicLit. Before the
// const-ident extension, N2 only flagged BasicLit first arguments; this fixture
// verifies that the typeseval.EvaluateConstString path also flags the call site.
package n2b_const_ident_reg_health

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
)

// storeReadyName is a package-level const — the bypass form that N2 must now catch.
const storeReadyName = "store_ready"

type myStore struct{}

func (s *myStore) RepoReady(_ context.Context) error { return nil }

// registerBadConst demonstrates the forbidden pattern using a const identifier
// as the first argument instead of a raw string literal.
// N2 must flag this via typeseval.EvaluateConstString resolution.
func registerBadConst(reg cell.Registry, s *myStore) {
	reg.Health(storeReadyName, s.RepoReady)
}
