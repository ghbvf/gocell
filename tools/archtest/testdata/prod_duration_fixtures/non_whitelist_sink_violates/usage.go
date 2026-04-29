// Package non_whitelist_sink_violates verifies that passing a literal duration
// to a non-standard (non-whitelist) function is still caught by the type-based
// gate: 1 violation expected.
package non_whitelist_sink_violates

import "time"

func myHelper(d time.Duration) {}

func f() {
	myHelper(5 * time.Second)
}
