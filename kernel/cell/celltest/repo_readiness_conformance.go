// Package celltest provides shared conformance harnesses for kernel/cell
// contracts. It is a normal package (imports testing) so every layer
// (cells/ internal, runtime/, adapters/) can hang the same single-source
// assertions off its own implementations.
package celltest

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/validation"
)

// RunRepoReadinessConformance is the single-source contract for
// cell.RepoHealthProber. Every RepoHealthProber implementation MUST be wired
// through this harness (CELL-REPO-READYZ-PROBE-01 archtest enforces the
// auto-join). It encodes the *differentiated* property that the typed funnel
// alone cannot: a SQL-backed RepoReady must FAIL when the cell's own
// relation(s) are gone, even though a pool-level ping would still pass.
//
//   - healthy: must be non-nil (including typed-nil). The harness calls
//     RepoReady(ctx) and asserts it returns nil.
//   - broken: RepoReady(ctx) != nil. SQL-backed implementations supply this
//     from a real PG with the cell's table(s) dropped (integration tag).
//     In-memory implementations have no differentiated failure domain; pass
//     a literal nil or a nil-valued RepoHealthProber to skip (recorded as a
//     skipped sub-test). SQL-backed implementations MUST pass a non-nil
//     broken prober.
//
// Note: broken is checked with [validation.IsNilInterface] so both untyped nil
// and typed-nil interface values (e.g. (*Foo)(nil)) are treated as absent.
func RunRepoReadinessConformance(t *testing.T, name string, healthy, broken cell.RepoHealthProber) {
	t.Helper()
	if validation.IsNilInterface(healthy) {
		t.Fatalf("RunRepoReadinessConformance: healthy must not be nil (name=%s)", name)
	}
	t.Run(name+"/healthy", func(t *testing.T) {
		if err := healthy.RepoReady(context.Background()); err != nil {
			t.Fatalf("RepoReady on healthy %s = %v, want nil", name, err)
		}
	})
	if validation.IsNilInterface(broken) {
		// In-memory store: always ready; nothing to break. Recorded as a
		// skipped sub-test so coverage of the contract stays visible.
		t.Run(name+"/schema-broken", func(t *testing.T) {
			t.Skip("in-memory implementation has no differentiated failure domain")
		})
		return
	}
	t.Run(name+"/schema-broken", func(t *testing.T) {
		if err := broken.RepoReady(context.Background()); err == nil {
			t.Fatalf("RepoReady on schema-broken %s = nil, want differentiated error "+
				"(schema/migration loss the pool ping cannot detect)", name)
		}
	})
}
