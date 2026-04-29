// Package alias_import_violates verifies that aliased time import does not
// bypass the gate: 1 violation expected.
package alias_import_violates

import t "time"

func f() {
	t.Sleep(5 * t.Second)
}
