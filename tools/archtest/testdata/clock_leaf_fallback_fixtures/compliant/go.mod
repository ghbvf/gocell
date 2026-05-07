module fixturetest/clock_leaf_fallback/compliant

go 1.25.10

// Pin to the worktree's kernel/clock so the fixture compiles against the same
// API the gate guards. The compliant fixture only consumes types/values it
// receives from constructors — no clock.Real() calls — so the gate must
// report 0 violations.
replace github.com/ghbvf/gocell => ../../../../..

require github.com/ghbvf/gocell v0.0.0
