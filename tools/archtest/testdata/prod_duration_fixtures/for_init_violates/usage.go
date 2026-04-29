// Package for_init_violates verifies that a literal duration in a for-loop
// initializer is caught: 1 violation expected.
package for_init_violates

import "time"

const defaultLimit = 10 * time.Second

func poll() {
	for d := 5 * time.Second; d < defaultLimit; d += time.Second {
		time.Sleep(time.Second)
	}
}
