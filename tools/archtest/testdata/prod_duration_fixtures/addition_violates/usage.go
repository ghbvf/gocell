// Package addition_violates verifies that an addition of two literal durations
// produces two violations (each operand is an independent matching expression):
// 2 violations expected.
package addition_violates

import "time"

var defaultWindow = 5*time.Second + 30*time.Millisecond
