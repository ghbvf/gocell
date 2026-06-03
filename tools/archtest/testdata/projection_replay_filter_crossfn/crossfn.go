// Package projection_replay_filter_crossfn is a blind-spot fixture for
// PROJECTION-REPLAY-PER-SPEC-FILTER-01: replayPhase delegates to a helper
// (applyGated) that holds the applyOne call, so applyOne does NOT appear in
// replayPhase's own AST subtree. The detector must therefore report
// sawApply=false for this package — exactly the anti-vacuity signal the
// production scan turns into a t.Fatal. This machine-verifies the
// "cross-function extraction" blind spot documented in the rule's godoc.
//
// DO NOT use this package in production code.
package projection_replay_filter_crossfn

type spec struct{ Topic string }

type entry struct{}

func (entry) RoutingTopic() string { return "" }

type coord struct {
	spec  spec
	apply func(entry) error
}

func (c *coord) applyOne(e entry, apply func(entry) error) error { return apply(e) }

func (c *coord) advanceOffsetPastForeign(_ entry) error { return nil }

// applyGated holds the applyOne call (with a real topic gate) — but it is a
// SEPARATE function, so replayPhase's subtree no longer contains applyOne.
func (c *coord) applyGated(e entry) error {
	if e.RoutingTopic() == c.spec.Topic {
		return c.applyOne(e, c.apply)
	}
	return c.advanceOffsetPastForeign(e)
}

// replayPhase delegates to applyGated — no direct applyOne call here.
func (c *coord) replayPhase(entries []entry) error {
	for _, e := range entries {
		if err := c.applyGated(e); err != nil {
			return err
		}
	}
	return nil
}
