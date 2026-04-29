// Package dot_import_violates verifies that dot-import of time does not
// bypass the gate: 1 violation expected.
package dot_import_violates

import . "time"

func f() {
	Sleep(5 * Second)
}
