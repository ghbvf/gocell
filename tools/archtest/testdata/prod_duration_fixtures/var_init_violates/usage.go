// Package var_init_violates verifies that a package-level var initializer
// with a literal duration is caught: 1 violation expected.
package var_init_violates

import "time"

var defaultWait = 5 * time.Second
