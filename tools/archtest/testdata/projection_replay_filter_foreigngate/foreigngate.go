// Package projection_replay_filter_foreigngate is a RED (reverse self-check)
// fixture for PROJECTION-REPLAY-PER-SPEC-FILTER-01: drainGap gates applyOne
// correctly, but the advanceOffsetPastForeign call is nested INSIDE the
// own-topic gate body instead of on the foreign fall-through. Foreign streams
// would then never advance the checkpoint, so a trailing foreign entry would
// stall catchup (#1574 C1). The detector must report ≥1 diagnostic (the foreign
// advance sits inside the gate); applyOne stays gated so sawApply=true.
//
// DO NOT use this package in production code.
package projection_replay_filter_foreigngate

type spec struct{ Topic string }

type entry struct{}

func (entry) Stream() string { return "" }

type coord struct {
	spec  spec
	apply func(entry) error
}

func (c *coord) applyOne(e entry, apply func(entry) error) error { return apply(e) }

func (c *coord) advanceOffsetPastForeign(_ entry) error { return nil }

// drainGap places advanceOffsetPastForeign INSIDE the own-topic gate — the
// violation: the foreign fall-through is empty, so foreign streams never advance
// the checkpoint.
func (c *coord) drainGap(entries []entry) error {
	for _, e := range entries {
		if e.Stream() == c.spec.Topic {
			if err := c.applyOne(e, c.apply); err != nil {
				return err
			}
			if err := c.advanceOffsetPastForeign(e); err != nil {
				return err
			}
		}
	}
	return nil
}
