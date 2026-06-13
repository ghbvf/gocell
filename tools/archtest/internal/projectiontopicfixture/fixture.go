//go:build archtest_fixture

// Package projectiontopicfixture is the RED fixture for
// PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01. It constructs the journaling
// decorator with a HAND-TYPED []string topic literal instead of the cellgen-derived
// generatedProjectionSourceTopics() accessor — the exact drift the invariant forbids
// (a hand-maintained allowlist would silently diverge from slice.yaml projections).
// The detector run against this package must flag the call.
package projectiontopicfixture

import adapterpg "github.com/ghbvf/gocell/adapters/postgres"

// handTypedTopics passes a hand-typed literal as the projection-source topic set.
func handTypedTopics() {
	var base *adapterpg.OutboxWriter
	_ = adapterpg.NewJournalingOutboxWriter(base, []string{"event.hand.typed.v1"})
}
