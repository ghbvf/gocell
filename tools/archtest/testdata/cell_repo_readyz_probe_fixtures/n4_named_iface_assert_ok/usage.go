// Package n4_named_iface_assert_ok exercises the N1 GREEN reverse fixture for
// CELL-REPO-READYZ-PROBE-01: a type-assertion to the NAMED interface
// cell.HealthProber. The rule must NOT flag this because it is not an anonymous
// interface with a Health method — it is an assertion to a named exported type.
package n4_named_iface_assert_ok

import (
	"github.com/ghbvf/gocell/kernel/cell"
)

// assertNamedHealthProber performs a named-interface assertion to cell.HealthProber.
// N1 must NOT flag this.
func assertNamedHealthProber(v any) cell.HealthProber {
	if hp, ok := v.(cell.HealthProber); ok {
		return hp
	}
	return nil
}
