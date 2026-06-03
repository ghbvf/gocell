module fixturetest/clock_positional_injection/selector_violates

go 1.25.11

// Pin to the worktree's kernel/clock so the fixture can resolve
// kernel/clock.MustHaveClock and exercise the type-aware positional-injection gate.
replace github.com/ghbvf/gocell => ../../../../..

require github.com/ghbvf/gocell v0.0.0
