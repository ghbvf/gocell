module fixturetest/errcode_prefix_ownership_selector

go 1.25.11

// Pin to the worktree's pkg/errcode so the fixture's errcode.Code type and
// errcode.OwnerOfCode registry resolve via go/types — the SelectorExpr /
// Ident sentinel red cases need typed const evaluation (EvaluateConstString),
// which is only available under StandaloneModule (typed) loading.
replace github.com/ghbvf/gocell => ../../../..

require github.com/ghbvf/gocell v0.0.0
