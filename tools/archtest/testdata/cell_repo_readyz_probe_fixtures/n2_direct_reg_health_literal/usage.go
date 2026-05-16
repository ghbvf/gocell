// Package n2_direct_reg_health_literal exercises the N2 RED fixture for
// CELL-REPO-READYZ-PROBE-01: a direct reg.Health(stringLiteral, fn) call where
// the first argument is a *ast.BasicLit string. The rule must flag this because
// repo-readiness must go through cell.RegisterRepoReadiness, not a bare
// reg.Health call.
package n2_direct_reg_health_literal

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
)

type myStore struct{}

func (s *myStore) RepoReady(_ context.Context) error { return nil }

// registerBad demonstrates the forbidden pattern: calling reg.Health with a
// string literal first argument to register a repo-readiness probe.
// N2 must catch this call site.
func registerBad(reg cell.Registry, s *myStore) {
	reg.Health("store_ready", s.RepoReady)
}
