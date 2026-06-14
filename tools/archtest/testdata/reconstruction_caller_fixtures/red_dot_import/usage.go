// Package red_dot_import is a RED fixture for OUTBOX-RECONSTRUCTION-CALLER-01:
// a dot-imported bare-identifier call to UnmarshalEnvelope (a bare *ast.Ident,
// not a pkg.Sel SelectorExpr) from a non-sanctioned site must be flagged by the
// forward scan's Ident branch. Without the bare-Ident walk this call is invisible
// and the rule silently passes — the dot-import blind spot the "Hard downstream"
// claim must not have.
package red_dot_import

import . "github.com/ghbvf/gocell/framework/kernel/outbox"

// reconstruct invokes the dot-imported UnmarshalEnvelope directly
// (bare-identifier form). Returning Entry only NAMES the sealed type — it never
// constructs a populated literal, so the fixture compiles.
func reconstruct() (Entry, error) {
	return UnmarshalEnvelope("topic", nil)
}
