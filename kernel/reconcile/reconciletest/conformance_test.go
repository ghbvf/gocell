// Package reconciletest_test is the self-check for RunConformance (T29).
// It runs the full conformance suite against fakes-backed harnesses for both
// Features{} and Features{Leader:true, Fencing:true}, proving the harness is
// non-vacuous and the fakes pass every contract.
//
// Using an external test package (reconciletest_test) avoids import cycles:
// reconciletest imports kernel/reconcile, and building Loops via the public
// Builder from within the same package is natural from an external package.
package reconciletest_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/kernel/reconcile/reconciletest"
)

// TestRunConformance_NoLeader exercises all non-leader subtests with a
// fakes-backed harness (Features{} — Leader=false, Fencing=false).
func TestRunConformance_NoLeader(t *testing.T) {
	reconciletest.RunConformance(t, noLeaderHarness, reconciletest.Features{})
}

// TestRunConformance_WithLeaderAndFencing exercises all subtests (including
// LeaderFlow and Fencing) with fakes-backed leader + fenced repo
// (Features{Leader:true, Fencing:true}).
func TestRunConformance_WithLeaderAndFencing(t *testing.T) {
	reconciletest.RunConformance(t, leaderFencingHarness, reconciletest.Features{
		Leader:  true,
		Fencing: true,
	})
}

// noLeaderHarness builds a Wiring with no Leader/Fenced (single-process mode).
// Each call to NewTrigger returns a fresh FakeTrigger so subtests are isolated.
func noLeaderHarness(t *testing.T) reconciletest.Wiring {
	t.Helper()
	return reconciletest.Wiring{
		NewTrigger: func() (reconcile.Trigger, func(reconcile.Request)) {
			return reconciletest.NewFakeTrigger()
		},
	}
}

// leaderFencingHarness builds a Wiring with a FakeLeaseBackend-backed leader
// and a FakeFencedRepository.
func leaderFencingHarness(t *testing.T) reconciletest.Wiring {
	t.Helper()
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	repo := reconciletest.NewFakeFencedRepository()
	return reconciletest.Wiring{
		NewTrigger: func() (reconcile.Trigger, func(reconcile.Request)) {
			return reconciletest.NewFakeTrigger()
		},
		Leader:  backend.Elector("conf-holder-A"),
		Fenced:  repo,
		Cleanup: nil,
	}
}
