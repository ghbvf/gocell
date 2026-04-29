// Package var_basicLit_violates verifies that a bare numeric BasicLit assigned
// to a time.Duration variable is caught (single-node BasicLit case): 1 violation.
package var_basicLit_violates

import "time"

var defaultWait time.Duration = 5
