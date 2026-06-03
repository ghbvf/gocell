module fixturetest/clock_positional_injection/struct_field_unexported_ok

go 1.25.11

// Pin to the worktree's kernel/clock so the fixture uses the canonical
// clock.Clock type for the typed predicate in sub-check C.
replace github.com/ghbvf/gocell => ../../../../..

require github.com/ghbvf/gocell v0.0.0
