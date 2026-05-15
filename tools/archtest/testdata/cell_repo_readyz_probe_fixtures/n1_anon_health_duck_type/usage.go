// Package n1_anon_health_duck_type exercises the N1 RED fixture for
// CELL-REPO-READYZ-PROBE-01: a type-assertion to an anonymous interface that
// exposes Health(context.Context) error. The rule must flag this call site.
package n1_anon_health_duck_type

import "context"

// repoLike is a pretend store whose Health method mirrors the old duck-type
// pattern that CELL-REPO-READYZ-PROBE-01 forbids.
type repoLike struct{}

func (r *repoLike) Health(_ context.Context) error { return nil }

// register performs the forbidden anonymous duck-type assertion.
// N1 must catch this: the asserted type is an anonymous interface{ Health(context.Context) error }.
func register(v any) {
	if h, ok := v.(interface {
		Health(context.Context) error
	}); ok {
		_ = h.Health(context.Background())
	}
}
