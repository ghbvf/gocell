// Package return_violates verifies that returning a literal duration from a
// function body is caught: 1 violation expected (declared via spec.Violation()).
package return_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func defaultTimeout() time.Duration {
	spec.Violation()
	return 5 * time.Second
}
