// Package p1_impl_no_conformance exercises the P1 RED fixture for
// CELL-REPO-READYZ-PROBE-01: a concrete type that implements
// cell.RepoHealthProber but has no RunRepoReadinessConformance call in the
// test corpus. The rule must flag this implementation.
package p1_impl_no_conformance

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
)

// compile-time proof that OrphanStore implements cell.RepoHealthProber.
var _ cell.RepoHealthProber = (*OrphanStore)(nil)

// OrphanStore implements cell.RepoHealthProber but is intentionally NOT wired
// into any RunRepoReadinessConformance call. P1 must flag this type.
type OrphanStore struct{}

// RepoReady implements cell.RepoHealthProber.
func (s *OrphanStore) RepoReady(_ context.Context) error { return nil }
