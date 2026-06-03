module fixturetest/clock_positional_injection/aliased_import_selector_violates

go 1.25.11

// Pin to the worktree's kernel/clock so the fixture uses the canonical
// clock.Clock type and clock.MustHaveClock resolves via go/types even with an
// import alias.
replace github.com/ghbvf/gocell => ../../../../..

require github.com/ghbvf/gocell v0.0.0
