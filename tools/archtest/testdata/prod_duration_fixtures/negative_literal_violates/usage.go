// Package negative_literal_violates verifies that a negative literal duration
// expression (-5*time.Second) is caught: 1 violation expected.
package negative_literal_violates

import "time"

var defaultOffset = -5 * time.Second
