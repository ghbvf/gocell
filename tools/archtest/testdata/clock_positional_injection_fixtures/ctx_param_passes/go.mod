module fixturetest/clock_positional_injection/ctx_param_passes

go 1.25.10

// Pin to the worktree's kernel/clock so the fixture uses the canonical
// clock.Clock type and clock.MustHaveClock resolves via go/types.
replace github.com/ghbvf/gocell => ../../../../..

require github.com/ghbvf/gocell v0.0.0
