// Package composite_field_violates verifies that a literal duration in a
// composite literal field is caught:
// 1 violation expected (declared via spec.Violation()).
package composite_field_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

type Config struct {
	Timeout time.Duration
}

func init() { spec.Violation() }

var defaultConfig = Config{
	Timeout: 5 * time.Second,
}
