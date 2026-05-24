// Package decl_bypass_red is a testdata fixture for
// OPS-CONTRACT-STRING-FUNNEL-01 blind spot #3: a ReadyProbeName const declared
// in a package outside the sanctioned ready-probe package set is a
// declaration-site bypass. The declaration-site guard (package ∉
// readyProbeSanctionedPkgs) must flag it. Uses a local ReadyProbeName mirror
// type so the test isolates the sanctioned-package guard from the production
// kernel/healthz type filter.
package decl_bypass_red

// ReadyProbeName mirrors kernel/healthz.ReadyProbeName.
type ReadyProbeName string

// probeBad is declared outside any sanctioned adapter/runtime package — the
// guard must flag it.
const probeBad ReadyProbeName = "bypass_ready"
