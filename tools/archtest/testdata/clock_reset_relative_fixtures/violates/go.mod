module fixturetest/clock_reset_relative/violates

go 1.25.10

// Pin to the worktree so the fixture can resolve fixturespec.Violation.
replace github.com/ghbvf/gocell => ../../../../..

require github.com/ghbvf/gocell v0.0.0
