// Package projection_replay_filter_green is a GREEN fixture for
// PROJECTION-REPLAY-PER-SPEC-FILTER-01: replayPhase invokes applyOne ONLY inside
// the `entry.RoutingTopic() == c.spec.Topic` gate, mirroring the production
// kernel/projection/rebuild.go shape. The detector must report 0 diagnostics.
//
// DO NOT use this package in production code.
package projection_replay_filter_green

type spec struct{ Topic string }

type entry struct{}

func (entry) RoutingTopic() string { return "" }

type coord struct {
	spec  spec
	apply func(entry) error
}

func (c *coord) applyOne(e entry, apply func(entry) error) error { return apply(e) }

func (c *coord) advanceOffsetPastForeign(_ entry) error { return nil }

// replayPhase gates applyOne behind the per-spec topic check.
func (c *coord) replayPhase(entries []entry) error {
	for _, e := range entries {
		if e.RoutingTopic() == c.spec.Topic {
			if err := c.applyOne(e, c.apply); err != nil {
				return err
			}
			continue
		}
		if err := c.advanceOffsetPastForeign(e); err != nil {
			return err
		}
	}
	return nil
}
