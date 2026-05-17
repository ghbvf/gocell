// Package negative_literal_violates verifies that a negative literal duration
// expression (-5*time.Second) is caught:
// 1 violation expected (declared via spec.Violation()).
package negative_literal_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func init() { spec.Violation() }

var defaultOffset = -5 * time.Second
