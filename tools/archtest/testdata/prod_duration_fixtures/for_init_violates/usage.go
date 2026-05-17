// Package for_init_violates verifies that a literal duration in a for-loop
// initializer is caught: 1 violation expected (declared via spec.Violation()).
package for_init_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

const defaultLimit = 10 * time.Second

func poll() {
	spec.Violation()
	for d := 5 * time.Second; d < defaultLimit; d += time.Second {
		time.Sleep(time.Second)
	}
}
