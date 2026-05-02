// Package newticker_violates verifies that time.NewTicker is flagged: 1 violation expected.
package newticker_violates

import "time"

func loop(interval time.Duration, stop <-chan struct{}) {
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
