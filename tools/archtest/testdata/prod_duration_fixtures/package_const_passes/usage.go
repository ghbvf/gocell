// Package package_const_passes verifies that a package-level const initializer
// is the unique compliant position: no violation expected.
package package_const_passes

import "time"

const defaultTimeout = 5 * time.Second

func doWork() {
	time.Sleep(defaultTimeout)
}
