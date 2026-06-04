// Package projection_replay_filter_red is a RED fixture for
// PROJECTION-REPLAY-PER-SPEC-FILTER-01: drainGap invokes applyOne
// UNCONDITIONALLY (no `entry.RoutingTopic() == c.spec.Topic` gate), so the
// business Apply would run for foreign streams during a rebuild. The detector
// must report ≥1 diagnostic.
//
// DO NOT use this package in production code.
package projection_replay_filter_red

type spec struct{ Topic string }

type entry struct{}

func (entry) RoutingTopic() string { return "" }

type coord struct {
	spec  spec
	apply func(entry) error
}

func (c *coord) applyOne(e entry, apply func(entry) error) error { return apply(e) }

// drainGap calls applyOne with no per-spec gate — the violation.
func (c *coord) drainGap(entries []entry) error {
	for _, e := range entries {
		if err := c.applyOne(e, c.apply); err != nil {
			return err
		}
	}
	return nil
}
