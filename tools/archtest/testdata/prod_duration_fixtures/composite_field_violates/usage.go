// Package composite_field_violates verifies that a literal duration in a
// composite literal field is caught: 1 violation expected.
package composite_field_violates

import "time"

type Config struct {
	Timeout time.Duration
}

var defaultConfig = Config{
	Timeout: 5 * time.Second,
}
