module fixturetest/clock_leaf_fallback/violates

go 1.25.10

// Pin to the worktree's kernel/clock so the fixture can resolve
// kernel/clock.Real() and exercise the type-aware leaf-fallback gate.
replace github.com/ghbvf/gocell => ../../../../..

require github.com/ghbvf/gocell v0.0.0
