// Package newticker_violates verifies that time.NewTicker is flagged:
// 1 violation expected (declared via spec.Violation()).
package newticker_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func loop(interval time.Duration, stop <-chan struct{}) {
	spec.Violation()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
