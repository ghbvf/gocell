// Package closure_violates verifies that a literal duration inside a closure
// body is caught: 1 violation expected.
package closure_violates

import "time"

var doWork = func() {
	time.Sleep(5 * time.Second)
}
