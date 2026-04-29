// Package chained_unit_violates verifies that a chained magnitude expression
// like 7*24*time.Hour is caught as one violation: 1 violation expected.
package chained_unit_violates

import "time"

var defaultRetention = 7 * 24 * time.Hour
